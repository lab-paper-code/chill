// Command publish stores E5's private raw evidence before its immutable summary.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

const maxPublicationJSONBytes = 4 << 20

type publicationConfig struct {
	LogicalStore    string `json:"logicalStore"`
	Bucket          string `json:"bucket"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyID"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
	TempDir         string `json:"tempDir,omitempty"`
}

type publicationObject struct {
	Store     string `json:"store"`
	Key       string `json:"key"`
	LocalPath string `json:"localPath"`
}

type publicationManifest struct {
	Objects []publicationObject `json:"objects"`
	Summary publicationObject   `json:"summary"`
}

func main() {
	configPath := flag.String("config", "", "private S3 store configuration JSON")
	manifestPath := flag.String("manifest", "", "private raw-object and final-summary manifest JSON")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *configPath, *manifestPath, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath, manifestPath string, output io.Writer) error {
	if configPath == "" || manifestPath == "" {
		return errors.New("--config and --manifest are required")
	}
	var config publicationConfig
	if err := readPublicationJSON(configPath, &config); err != nil {
		return err
	}
	var manifest publicationManifest
	if err := readPublicationJSON(manifestPath, &manifest); err != nil {
		return err
	}
	store, err := s3.New(s3.Config{
		LogicalStore: config.LogicalStore, Bucket: config.Bucket, Endpoint: config.Endpoint, Region: config.Region,
		AccessKeyID: config.AccessKeyID, SecretAccessKey: config.SecretAccessKey, SessionToken: config.SessionToken, TempDir: config.TempDir,
	})
	if err != nil {
		return err
	}
	receipt, err := publish(ctx, store, config.LogicalStore, manifest)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(receipt)
}

func readPublicationJSON(name string, value any) (err error) {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	limited := &io.LimitedReader{R: file, N: maxPublicationJSONBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("publication input must contain one JSON object")
	}
	if limited.N == 0 {
		return errors.New("publication JSON exceeds byte limit")
	}
	return nil
}

func publish(ctx context.Context, writer evidencestore.ImmutableWriter, store string, manifest publicationManifest) (evidencestore.Receipt, error) {
	var summary httpapi.ExperimentSummary
	if err := readPublicationJSON(manifest.Summary.LocalPath, &summary); err != nil {
		return evidencestore.Receipt{}, err
	}
	if err := httpapi.ValidateExperimentSummary(summary, store, summary.ID); err != nil {
		return evidencestore.Receipt{}, err
	}
	prefix := httpapi.ExperimentPrefix + summary.ID + "/"
	if manifest.Summary.Store != store || manifest.Summary.Key != prefix+"summary.json" || manifest.Objects == nil {
		return evidencestore.Receipt{}, errors.New("publication manifest must match the experiment summary identity")
	}
	seen := map[string]bool{manifest.Summary.Key: true}
	for _, object := range manifest.Objects {
		if object.Store != store || !strings.HasPrefix(object.Key, prefix) || path.Clean(object.Key) != object.Key ||
			strings.ContainsAny(object.Key, "\\:%?#\x00\r\n\t") || strings.TrimSpace(object.Key) != object.Key || seen[object.Key] {
			return evidencestore.Receipt{}, errors.New("raw publication objects must be unique safe keys within this experiment")
		}
		seen[object.Key] = true
	}
	for _, source := range summary.Sources {
		if source.Key == manifest.Summary.Key || !seen[source.Key] {
			return evidencestore.Receipt{}, errors.New("summary sources must identify raw evidence published by this manifest")
		}
	}
	// Validate every local file before any upload; a missing raw artifact cannot
	// leave a discoverable final summary.
	for _, object := range manifest.Objects {
		if _, err := publicationIdentity(object); err != nil {
			return evidencestore.Receipt{}, err
		}
	}
	for _, object := range manifest.Objects {
		receipt, err := putPublicationObject(ctx, writer, object)
		if err != nil {
			return evidencestore.Receipt{}, err
		}
		if receipt.Outcome != evidencestore.CreateOutcomeCreated {
			return evidencestore.Receipt{}, fmt.Errorf("%w: experiment raw evidence already exists; publication requires a new id", evidencestore.ErrPrecondition)
		}
	}
	receipt, err := putPublicationObject(ctx, writer, manifest.Summary)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	if receipt.Outcome != evidencestore.CreateOutcomeCreated {
		return evidencestore.Receipt{}, fmt.Errorf("%w: experiment id already published; corrections require a new id", evidencestore.ErrPrecondition)
	}
	return receipt, nil
}

func publicationIdentity(object publicationObject) (evidencestore.ObjectIdentity, error) {
	stat, err := os.Stat(object.LocalPath)
	if err != nil {
		return evidencestore.ObjectIdentity{}, err
	}
	identity := evidencestore.ObjectIdentity{Store: object.Store, Key: object.Key, Size: stat.Size()}
	if !stat.Mode().IsRegular() {
		return identity, errors.New("publication input must be a regular file")
	}
	return identity, identity.Validate()
}

func putPublicationObject(ctx context.Context, writer evidencestore.ImmutableWriter, object publicationObject) (receipt evidencestore.Receipt, err error) {
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	file, err := os.Open(object.LocalPath)
	if err != nil {
		return receipt, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	stat, err := file.Stat()
	if err != nil {
		return receipt, err
	}
	identity := evidencestore.ObjectIdentity{Store: object.Store, Key: object.Key, Size: stat.Size()}
	return writer.PutImmutable(ctx, identity, file)
}
