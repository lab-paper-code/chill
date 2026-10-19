package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"text/tabwriter"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

type profileClient struct{ *ownerClient }

func newProfileOperations(opts operationOptions) (*profileClient, error) {
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

func (q *profileClient) List(ctx context.Context) (httpapi.ProfilesView, error) {
	var result httpapi.ProfilesView
	err := q.read(ctx, http.MethodGet, "/api/v1/scheduler/profiles", nil, "", maximumEvidencePassportBytes, &result)
	if err == nil && result.ConfigVersion == "" {
		return result, errors.New("owner returned an unexpected profile view")
	}
	return result, err
}

func executeProfiles(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 || args[0] != operationList {
		return fmt.Errorf("%w: profiles requires list", errUsage)
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
	result, err := q.List(ctx)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	if opts.output == outputFormatJSON {
		return writeOperationJSON(stdout, result)
	}
	return writeProfilesTable(stdout, result)
}

func writeProfilesTable(writer io.Writer, view httpapi.ProfilesView) error {
	table := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(table, "CONFIG_VERSION\t%s\n\nPROFILE_ID\tNODE\tBACKEND\tMEASURED_AT\n",
		display(view.ConfigVersion)); err != nil {
		return err
	}
	for _, p := range view.Profiles {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", p.ID, p.NodeName, p.Condition.Backend,
			p.MeasuredAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(table, "\nSOURCE\tCODE\tREASON\n"); err != nil {
		return err
	}
	for _, issue := range view.Insufficiencies {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", issue.Source, issue.Code, issue.Reason); err != nil {
			return err
		}
	}
	return table.Flush()
}
