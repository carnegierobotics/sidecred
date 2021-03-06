// Package github implements a sidecred.Provider for Github access tokens and deploy keys. It also implements
// a client for Github Apps, which is used to create the supported credentials.
package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/telia-oss/sidecred"

	"github.com/google/go-github/v28/github"
	"golang.org/x/crypto/ssh"
	"golang.org/x/oauth2"
)

// DeployKeyRequestConfig ...
type DeployKeyRequestConfig struct {
	Owner      string `json:"owner"`
	Repository string `json:"repository"`
	Title      string `json:"title"`
	ReadOnly   bool   `json:"read_only"`
}

// AccessTokenRequestConfig ...
type AccessTokenRequestConfig struct {
	Owner string `json:"owner"`
}

// New returns a new sidecred.Provider for Github credentials.
func New(client AppsAPI, options ...option) sidecred.Provider {
	p := &provider{
		app:                 newApp(client),
		keyRotationInterval: time.Duration(time.Hour * 24 * 7),
		reposClientFactory:  defaultReposClientFactory,
	}
	for _, optionFunc := range options {
		optionFunc(p)
	}
	return p
}

type option func(*provider)

// WithDeployKeyRotationInterval sets the interval at which deploy keys should be rotated.
func WithDeployKeyRotationInterval(duration time.Duration) option {
	return func(p *provider) {
		p.keyRotationInterval = duration
	}
}

// WithReposClientFactory sets the function used to create new installation clients, and can be used to return test fakes.
func WithReposClientFactory(f func(token string) RepositoriesAPI) option {
	return func(p *provider) {
		p.reposClientFactory = f
	}
}

func defaultReposClientFactory(token string) RepositoriesAPI {
	oauth := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	))
	client := github.NewClient(oauth)
	return client.Repositories
}

// Implements sidecred.Provider for Github Credentials.
type provider struct {
	app                 *app
	reposClientFactory  func(token string) RepositoriesAPI
	keyRotationInterval time.Duration
}

// Type implements sidecred.Provider.
func (p *provider) Type() sidecred.ProviderType {
	return sidecred.Github
}

// Create implements sidecred.Provider.
func (p *provider) Create(request *sidecred.Request) ([]*sidecred.Credential, *sidecred.Metadata, error) {
	switch request.Type {
	case sidecred.GithubDeployKey:
		return p.createDeployKey(request)
	case sidecred.GithubAccessToken:
		return p.createAccessToken(request)
	}
	return nil, nil, fmt.Errorf("invalid request: %s", request.Type)
}

func (p *provider) createAccessToken(request *sidecred.Request) ([]*sidecred.Credential, *sidecred.Metadata, error) {
	var c AccessTokenRequestConfig
	if err := request.UnmarshalConfig(&c); err != nil {
		return nil, nil, err
	}
	userToken, expiration, err := p.app.createInstallationToken(c.Owner, &github.InstallationPermissions{
		Metadata:     github.String("read"),
		Contents:     github.String("read"),
		PullRequests: github.String("write"),
		Statuses:     github.String("write"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create access token: %s", err)
	}
	return []*sidecred.Credential{{
		Name:        c.Owner + "-access-token",
		Value:       userToken,
		Description: "Github access token managed by sidecred.",
		Expiration:  expiration,
	}}, nil, nil
}

func (p *provider) createDeployKey(request *sidecred.Request) ([]*sidecred.Credential, *sidecred.Metadata, error) {
	var c DeployKeyRequestConfig
	if err := request.UnmarshalConfig(&c); err != nil {
		return nil, nil, err
	}
	adminToken, _, err := p.app.createInstallationToken(c.Owner, &github.InstallationPermissions{
		Administration: github.String("write"), // Used to add deploy keys to repositories: https://developer.github.com/v3/apps/permissions/#permission-on-administration
		Metadata:       github.String("read"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create administrator access token: %s", err)
	}

	privateKey, publicKey, err := p.generateKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generate key pair: %s", err)
	}

	key, _, err := p.reposClientFactory(adminToken).CreateKey(context.TODO(), c.Owner, c.Repository, &github.Key{
		ID:       nil,
		Key:      github.String(publicKey),
		URL:      nil,
		Title:    github.String(c.Title),
		ReadOnly: github.Bool(c.ReadOnly),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create deploy key: %s", err)
	}

	metadata := &sidecred.Metadata{"key_id": strconv.Itoa(int(key.GetID()))}
	return []*sidecred.Credential{{
		Name:        c.Repository + "-deploy-key",
		Value:       privateKey,
		Description: "Github deploy key managed by sidecred.",
		Expiration:  key.GetCreatedAt().Add(p.keyRotationInterval).UTC(),
	}}, metadata, nil
}

func (p *provider) generateKeyPair() (string, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}

	privateKey := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicKey := ssh.MarshalAuthorizedKey(pub)
	return string(privateKey), string(publicKey), nil
}

// Destroy implements sidecred.Provider.
func (p *provider) Destroy(resource *sidecred.Resource) error {
	var c DeployKeyRequestConfig
	if err := json.Unmarshal(resource.Config, &c); err != nil {
		return fmt.Errorf("unmarshal resource config: %s", err)
	}
	if resource.Metadata == nil {
		return nil
	}
	s := (*resource.Metadata)["key_id"]
	if s == "" {
		return nil
	}
	keyID, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to convert key id (%s) to int: %s", s, err)
	}
	adminToken, _, err := p.app.createInstallationToken(c.Owner, &github.InstallationPermissions{
		Administration: github.String("write"), // Used to add deploy keys to repositories: https://developer.github.com/v3/apps/permissions/#permission-on-administration
		Metadata:       github.String("read"),
	})
	if err != nil {
		return fmt.Errorf("create administrator access token: %s", err)
	}
	resp, err := p.reposClientFactory(adminToken).DeleteKey(context.TODO(), c.Owner, c.Repository, keyID)
	if err != nil && resp.StatusCode != http.StatusNotFound {
		// Ignore error if statuscode is 404 (key not found)
		return fmt.Errorf("delete deploy key: %s", err)
	}
	return nil
}
