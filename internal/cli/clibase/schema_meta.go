package clibase

import (
	"reflect"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

const (
	SchemaGroupAnnotation      = "gitslice.io/schema-group"
	SchemaExcludedAnnotation   = "gitslice.io/schema-excluded"
	SchemaCollapsedAnnotation  = "gitslice.io/schema-collapsed"
	SchemaUseAnnotation        = "gitslice.io/schema-use"
	SchemaSummaryAnnotation    = "gitslice.io/schema-summary"
	SchemaOutputAnnotation     = "gitslice.io/schema-output"
	SchemaOutputSetAnnotation  = "gitslice.io/schema-output-set"
	SchemaOutputTypeAnnotation = "gitslice.io/schema-output-type"
)

var (
	schemaOutputTypesMu sync.RWMutex
	schemaOutputTypes   = map[string]reflect.Type{}
)

func registerSchemaOutputType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	key := t.String()
	if t.PkgPath() != "" && t.Name() != "" {
		key = t.PkgPath() + "." + t.Name()
	}
	schemaOutputTypesMu.Lock()
	schemaOutputTypes[key] = t
	schemaOutputTypesMu.Unlock()
	return key
}

func SchemaOutputTypeForCommand(cmd *cobra.Command) (reflect.Type, bool) {
	if cmd == nil {
		return nil, false
	}
	key := cmd.Annotations[SchemaOutputTypeAnnotation]
	if key == "" {
		return nil, false
	}
	schemaOutputTypesMu.RLock()
	t, ok := schemaOutputTypes[key]
	schemaOutputTypesMu.RUnlock()
	return t, ok
}

func ensureCommandAnnotations(cmd *cobra.Command) map[string]string {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	return cmd.Annotations
}

func MarkSchemaGroup(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd != nil {
			ensureCommandAnnotations(cmd)[SchemaGroupAnnotation] = "true"
		}
	}
}

func MarkSchemaExcluded(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd != nil {
			ensureCommandAnnotations(cmd)[SchemaExcludedAnnotation] = "true"
		}
	}
}

func WithSchemaCollapsed(cmd *cobra.Command, use, summary string) {
	if cmd == nil {
		return
	}
	ann := ensureCommandAnnotations(cmd)
	ann[SchemaCollapsedAnnotation] = "true"
	if use != "" {
		ann[SchemaUseAnnotation] = use
	}
	if summary != "" {
		ann[SchemaSummaryAnnotation] = summary
	}
}

func WithSchemaOutput[T any](cmds ...*cobra.Command) {
	t := reflect.TypeFor[T]()
	typeKey := registerSchemaOutputType(t)
	fields := StructJSONFields(t)
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		WithSchemaOutputFields(cmd, fields...)
		ensureCommandAnnotations(cmd)[SchemaOutputTypeAnnotation] = typeKey
	}
}

func WithSchemaOutputFields(cmd *cobra.Command, fields ...string) {
	if cmd == nil {
		return
	}
	ann := ensureCommandAnnotations(cmd)
	ann[SchemaOutputSetAnnotation] = "true"
	ann[SchemaOutputAnnotation] = strings.Join(fields, ",")
}

func IsSchemaGroupCommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Annotations[SchemaCollapsedAnnotation] == "true" {
		return false
	}
	if cmd.Annotations[SchemaGroupAnnotation] == "true" {
		return true
	}
	return !cmd.Runnable()
}

func IsSchemaExcludedCommand(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[SchemaExcludedAnnotation] == "true"
}

func IsSchemaCollapsedCommand(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[SchemaCollapsedAnnotation] == "true"
}

func StructJSONFields(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	var visit func(reflect.Type)
	visit = func(cur reflect.Type) {
		for cur.Kind() == reflect.Pointer {
			cur = cur.Elem()
		}
		if cur.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < cur.NumField(); i++ {
			field := cur.Field(i)
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if field.Anonymous && name == "" {
				visit(field.Type)
				continue
			}
			if !field.IsExported() {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	visit(t)
	return out
}
