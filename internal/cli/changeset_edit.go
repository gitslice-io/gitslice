package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// changesetDescriptionFlag reads a description given as --description or
// --description-file (- is standard input). Giving both is an error.
func changesetDescriptionFlag(stdin io.Reader, text, file string) (string, error) {
	if text != "" && file != "" {
		return "", userError("invalid_args", "pass --description or --description-file, not both", "")
	}
	if file == "" {
		return text, nil
	}
	var data []byte
	var err error
	if file == "-" {
		if stdin == nil {
			return "", userError("invalid_args", "--description-file - needs standard input", "")
		}
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return "", userError("description_unreadable", "cannot read the description: "+err.Error(), "")
	}
	return string(data), nil
}

// runChangesetEdit changes the title and description of an open changeset:
// the workspace's current one, or the one named.
func (r Runner) runChangesetEdit(ctx context.Context, opts commandOptions, requestedID string, title, description *string) error {
	if title == nil && description == nil {
		return userError("invalid_args", "nothing to change", "Pass --title, --description or --description-file.")
	}
	cfg, _, _, _, changesetID, _, err := r.resolveChangesetCommandState(requestedID)
	if err != nil {
		return err
	}
	_, conn, callCtx, err := r.authenticatedConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = cfg
	cs, err := corev1.NewChangesetServiceClient(conn).UpdateChangesetInfo(callCtx, &corev1.UpdateChangesetInfoRequest{
		ChangesetId: changesetID,
		Title:       title,
		Description: description,
	})
	if err != nil {
		return err
	}
	if opts.jsonOutput() {
		return r.writeJSONOutput(opts, map[string]any{
			"changeset":   firstNonEmpty(displayChangesetID(cs), cs.Id),
			"title":       cs.Title,
			"description": cs.Description,
		})
	}
	if !opts.Quiet {
		fmt.Fprintf(r.Stdout, "updated %s\n", firstNonEmpty(displayChangesetID(cs), cs.Id))
		fmt.Fprintf(r.Stdout, "title: %s\n", cs.Title)
		if cs.Description != "" {
			fmt.Fprintf(r.Stdout, "description:\n%s\n", indentLines(cs.Description, "  "))
		}
	}
	return nil
}

func optionalString(set bool, value string) *string {
	if !set {
		return nil
	}
	return &value
}
