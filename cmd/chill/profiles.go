package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

type profileOperations interface {
	List(context.Context) (httpapi.ProfileSelectionsView, error)
	Adopt(context.Context) (httpapi.ProfileSelectionResult, error)
	Unadopt(context.Context) (httpapi.ProfileSelectionResult, error)
}

type profileClient struct{ *ownerClient }

func newProfileOperations(opts operationOptions) (profileOperations, error) {
	config, namespace, err := loadKubernetesConfig(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	if opts.namespace == "" {
		opts.namespace = namespace
	}
	owner, err := newOwnerHTTPSClient(opts, config)
	if err != nil {
		return nil, err
	}
	return &profileClient{owner}, nil
}

func (q *profileClient) List(ctx context.Context) (httpapi.ProfileSelectionsView, error) {
	var result httpapi.ProfileSelectionsView
	err := q.read(ctx, http.MethodGet, "/api/v1/scheduler/profiles", nil, "", maximumEvidencePassportBytes, &result)
	if err == nil && result.ConfigVersion == "" {
		return result, errors.New("owner returned an unexpected profile selection view")
	}
	return result, err
}

func (q *profileClient) Adopt(ctx context.Context) (httpapi.ProfileSelectionResult, error) {
	var result httpapi.ProfileSelectionResult
	payload, err := json.Marshal(httpapi.ProfileSelectionRequest{
		Campaign: httpapi.ProfileCampaign{Namespace: q.opts.namespace, Name: q.opts.name},
	})
	if err != nil {
		return result, err
	}
	err = q.read(ctx, http.MethodPost, "/api/v1/scheduler/profiles", payload, "", maximumEvidencePassportBytes, &result)
	if err == nil && (result.Campaign.Namespace != q.opts.namespace || result.Campaign.Name != q.opts.name) {
		return result, errors.New("owner returned an unexpected measurement task identity")
	}
	return result, err
}

func (q *profileClient) Unadopt(ctx context.Context) (httpapi.ProfileSelectionResult, error) {
	var result httpapi.ProfileSelectionResult
	path := "/api/v1/scheduler/profiles/" + url.PathEscape(q.opts.namespace) + "/" + url.PathEscape(q.opts.name)
	err := q.read(ctx, http.MethodDelete, path, nil, q.opts.ifMatch, maximumEvidencePassportBytes, &result)
	if err == nil && (result.Campaign.Namespace != q.opts.namespace || result.Campaign.Name != q.opts.name) {
		return result, errors.New("owner returned an unexpected measurement task identity")
	}
	return result, err
}

func executeProfiles(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 || (args[0] != operationList && args[0] != "adopt" && args[0] != operationUnadopt) {
		return fmt.Errorf("%w: profiles requires list, adopt, or unadopt", errUsage)
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
	var result httpapi.ProfileSelectionResult
	if args[0] == operationUnadopt {
		result, err = q.Unadopt(ctx)
	} else {
		result, err = q.Adopt(ctx)
	}
	if err != nil {
		return fmt.Errorf("%s profile: %w", args[0], err)
	}
	if opts.output == outputFormatJSON {
		err = writeOperationJSON(stdout, result)
	} else {
		err = writeProfilesTable(stdout, result.ConfigVersion, []httpapi.ProfileSelectionResult{result})
	}
	if err != nil {
		return err
	}
	if args[0] == operationUnadopt {
		if result.Persisted || result.Loaded || result.Code == "ProfileUnloadFailed" ||
			(result.Status != "Removed" && result.Status != "Unselected") {
			return fmt.Errorf("profile unadoption incomplete (%s): %s", result.Code, result.Reason)
		}
		return nil
	}
	if !result.Persisted ||
		(result.Status != "Persisted" && result.Status != "Loaded" && result.Status != "AlreadySelected") {
		return fmt.Errorf("profile not adopted (%s): %s", result.Code, result.Reason)
	}
	return nil
}

func writeProfilesTable(writer io.Writer, version string, selections []httpapi.ProfileSelectionResult) error {
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
