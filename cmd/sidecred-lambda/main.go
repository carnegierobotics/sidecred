package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	environment "github.com/telia-oss/aws-env"
	"github.com/telia-oss/sidecred"
	"github.com/telia-oss/sidecred/internal/cli"

	"github.com/alecthomas/kingpin"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

var version string

func main() {
	var (
		app    = kingpin.New("sidecred", "Sideload your credentials.").Version(version).Writer(os.Stdout).DefaultEnvars()
		bucket = app.Flag("config-bucket", "Name of the S3 bucket where the config is stored.").Required().String()
	)

	sess, err := session.NewSession()
	if err != nil {
		panic(fmt.Errorf("failed to create a new session: %s", err))
	}

	// Exchange secrets in environment variables with their values.
	env, err := environment.New(sess)
	if err != nil {
		panic(fmt.Errorf("failed to initialize aws-env: %s", err))
	}

	if err := env.Populate(); err != nil {
		panic(fmt.Errorf("failed to populate environment: %s", err))
	}

	cli.Setup(app, runFunc(bucket), nil, nil)
	kingpin.MustParse(app.Parse(os.Args[1:]))
}

// Event is the expected payload sent to the Lambda.
type Event struct {
	Namespace  string `json:"namespace"`
	ConfigPath string `json:"config_path"`
	StatePath  string `json:"state_path"`
}

func runFunc(configBucket *string) func(*sidecred.Sidecred, sidecred.StateBackend) error {
	return func(s *sidecred.Sidecred, backend sidecred.StateBackend) error {
		lambda.Start(func(event Event) error {
			requests, err := loadConfig(*configBucket, event.ConfigPath)
			if err != nil {
				return err
			}
			state, err := backend.Load(event.StatePath)
			if err != nil {
				return err
			}
			defer backend.Save(event.StatePath, state)
			return s.Process(event.Namespace, requests, state)
		})
		return nil
	}
}

func loadConfig(bucket, key string) ([]*sidecred.Request, error) {
	sess, err := session.NewSession()
	if err != nil {
		return nil, err
	}
	client := s3.New(sess)

	var requests []*sidecred.Request
	obj, err := client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer obj.Body.Close()
	buf := bytes.NewBuffer(nil)
	if _, err := io.Copy(buf, obj.Body); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(buf.Bytes(), &requests); err != nil {
		return nil, err
	}
	return requests, nil
}
