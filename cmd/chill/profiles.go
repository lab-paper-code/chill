package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
	"k8s.io/client-go/rest"
)

type profileOperations interface {
	List(context.Context) (entrypoint.ProfileSelectionsView, error)
	Adopt(context.Context) (entrypoint.ProfileSelectionResult, error)
}

type profileClient struct {
	opts       operationOptions
	httpClient *http.Client
	origin     string
}

func newProfileOperations(opts operationOptions) (profileOperations, error) {
	config, namespace, err := loadKubernetesConfig(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	if opts.namespace == "" {
		opts.namespace = namespace
	}
	return newProfileHTTPSClient(opts, config)
}

func newProfileHTTPSClient(opts operationOptions, kubeConfig *rest.Config) (*profileClient, error) {
	origin := opts.servingURL
	if origin == "" {
		origin = "https://" + opts.servingService + "." + opts.namespace + ".svc:8080"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("serving-url must be one HTTPS origin without credentials, path, query or fragment")
	}
	// Owner credentials and trust are independent of the API server: never forward
	// kubeconfig client certificates, impersonation, insecure TLS, or auth plugins.
	config := &rest.Config{Host: origin, TLSClientConfig: rest.TLSClientConfig{CAFile: opts.servingCAFile}}
	if opts.tokenFile != "" {
		config.BearerTokenFile = opts.tokenFile
	} else {
		config.BearerToken = kubeConfig.BearerToken
		config.BearerTokenFile = kubeConfig.BearerTokenFile
	}
	if strings.TrimSpace(config.BearerToken) == "" && config.BearerTokenFile == "" {
		return nil, errors.New("owner bearer credentials required: use --token-file or kubeconfig bearer credentials; " +
			"API-server client certificates do not authenticate to the owner")
	}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, fmt.Errorf("configure owner HTTPS transport: %w", err)
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &profileClient{opts: opts, httpClient: httpClient, origin: strings.TrimSuffix(origin, "/")}, nil
}

func (q *profileClient) read(ctx context.Context, verb string, payload []byte, result any) error {
	request, err := http.NewRequestWithContext(ctx, verb, q.origin+"/api/v1/scheduler/profiles", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := q.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request owner HTTPS API: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maximumEvidencePassportBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if int64(len(body)) > maximumEvidencePassportBytes {
		return errors.New("owner response exceeds size limit")
	}
	if response.StatusCode == http.StatusUnauthorized {
		return errors.New("owner bearer credentials rejected (HTTP 401); supply a valid caller token with --token-file")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("owner HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, result)
}

func (q *profileClient) List(ctx context.Context) (entrypoint.ProfileSelectionsView, error) {
	var result entrypoint.ProfileSelectionsView
	err := q.read(ctx, http.MethodGet, nil, &result)
	return result, err
}

func (q *profileClient) Adopt(ctx context.Context) (entrypoint.ProfileSelectionResult, error) {
	var result entrypoint.ProfileSelectionResult
	payload, err := json.Marshal(entrypoint.ProfileSelectionRequest{
		Campaign: entrypoint.ProfileCampaign{Namespace: q.opts.namespace, Name: q.opts.name},
	})
	if err != nil {
		return result, err
	}
	err = q.read(ctx, http.MethodPost, payload, &result)
	if err == nil && (result.Campaign.Namespace != q.opts.namespace || result.Campaign.Name != q.opts.name) {
		return result, errors.New("owner returned an unexpected measurement task identity")
	}
	return result, err
}

func executeProfiles(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 || (args[0] != operationList && args[0] != "adopt") {
		return fmt.Errorf("%w: profiles requires list or adopt", errUsage)
	}
	opts, err := parseOperationOptions(profilesCommand, args[0], args[1:], stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	q, err := deps.newProfiles(opts)
	if err != nil {
		return err
	}
	if args[0] == operationList {
		result, err := q.List(ctx)
		if err != nil {
			return fmt.Errorf("list profiles: %w", err)
		}
		if opts.output == outputFormatJSON {
			return writeOperationJSON(stdout, result)
		}
		return writeProfilesTable(stdout, result.ConfigVersion, result.Selections)
	}
	result, err := q.Adopt(ctx)
	if err != nil {
		return fmt.Errorf("adopt profile: %w", err)
	}
	if opts.output == outputFormatJSON {
		err = writeOperationJSON(stdout, result)
	} else {
		err = writeProfilesTable(stdout, result.ConfigVersion, []entrypoint.ProfileSelectionResult{result})
	}
	if err != nil {
		return err
	}
	if !result.Persisted || result.Status == "Pending" || result.Status == "Rejected" {
		return fmt.Errorf("profile not adopted (%s): %s", result.Code, result.Reason)
	}
	return nil
}

func writeProfilesTable(writer io.Writer, version string, selections []entrypoint.ProfileSelectionResult) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	const header = "CONFIG_VERSION\t%s\n\n" +
		"NAMESPACE\tMEASUREMENT_TASK\tSTATUS\tPERSISTED\tLOADED\tPROFILE_ID\tCODE\tREASON\n"
	if _, err := fmt.Fprintf(table, header, display(version)); err != nil {
		return err
	}
	for _, item := range selections {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%t\t%t\t%s\t%s\t%s\n",
			item.Campaign.Namespace, item.Campaign.Name, item.Status, item.Persisted, item.Loaded,
			display(item.ProfileID), display(item.Code), display(item.Reason)); err != nil {
			return err
		}
	}
	return table.Flush()
}
