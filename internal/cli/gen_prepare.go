package cli

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const (
	rootPreparationID = "'0'"
)

var errBindPreparationOwner = errors.New("bind preparation owner is not numeric")

type bindPreparationOwnerError struct{}

func (*bindPreparationOwnerError) Error() string {
	return errBindPreparationOwner.Error()
}

func (*bindPreparationOwnerError) Unwrap() error {
	return errBindPreparationOwner
}

//go:embed templates/bind-preparation.sh.tmpl
var bindPreparationTemplateSource string

type bindPreparationTemplateData struct {
	UID   string
	GID   string
	Paths []string
}

func renderBindPreparation(workload domain.WorkloadSpec) ([]byte, error) {
	paths := make(map[string]struct{})
	for _, mount := range workload.Mounts {
		if mount.Kind != domain.MountBind || mount.ReadOnly {
			continue
		}
		if !filepath.IsAbs(mount.Source) || filepath.Clean(mount.Source) != mount.Source ||
			mount.Source == string(filepath.Separator) || strings.ContainsAny(mount.Source, "$\x00") {
			return nil, runtimeargv.ErrInvalid
		}
		paths[mount.Source] = struct{}{}
	}
	if len(paths) == 0 {
		return nil, nil
	}

	uid, gid, err := preparationOwner(workload.User)
	if err != nil {
		return nil, err
	}

	return executeBindPreparationTemplate(newBindPreparationTemplate(), bindPreparationTemplateData{
		UID: uid, GID: gid, Paths: slices.Sorted(maps.Keys(paths)),
	})
}

func newBindPreparationTemplate() *template.Template {
	return template.Must(template.New("bind-preparation.sh.tmpl").Funcs(template.FuncMap{
		"shellQuote": shellQuote,
	}).Parse(bindPreparationTemplateSource))
}

func executeBindPreparationTemplate(
	selected *template.Template,
	data bindPreparationTemplateData,
) ([]byte, error) {
	var script bytes.Buffer
	if err := selected.Execute(&script, data); err != nil {
		return nil, fmt.Errorf("render bind preparation template: %w", err)
	}

	return script.Bytes(), nil
}

func preparationOwner(user string) (string, string, error) {
	if user == "" {
		return rootPreparationID, rootPreparationID, nil
	}
	uidValue, gidValue, hasGroup := strings.Cut(user, ":")
	uid, uidValid := numericID(uidValue)
	gid, gidValid := numericID(gidValue)
	if !uidValid || !hasGroup || !gidValid {
		return "", "", &bindPreparationOwnerError{}
	}

	return shellQuote(uid), shellQuote(gid), nil
}

func numericID(value string) (string, bool) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return "", false
	}

	return strconv.FormatUint(parsed, 10), true
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
