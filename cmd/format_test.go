package cmd

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wisborg/fitactivity"

	"github.com/wisborg/fitdash/internal/inspect"
)

// Every field fitdash writes as JSON is named the same in YAML. The YAML
// library does not read a field's JSON name, and without a YAML name of its
// own a field was written as its Go name lowered. Walking every report type,
// nested ones included, catches a field added later with one name and not
// the other.
func TestYAMLNamesAreJSONNames(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var check func(reflect.Type)
	check = func(ty reflect.Type) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Map {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct || seen[ty] || ty.PkgPath() != reflect.TypeOf(inspectJSON{}).PkgPath() {
			return
		}
		seen[ty] = true
		for i := range ty.NumField() {
			f := ty.Field(i)
			j, ok := f.Tag.Lookup("json")
			if !ok && f.IsExported() {
				t.Errorf("%s.%s has no JSON name", ty.Name(), f.Name)
				continue
			}
			if y, _ := f.Tag.Lookup("yaml"); ok && y != j {
				t.Errorf("%s.%s is %q in JSON and %q in YAML", ty.Name(), f.Name, j, y)
			}
			check(f.Type)
		}
	}
	for _, v := range []any{inspectJSON{}, fetchReport{}} {
		check(reflect.TypeOf(v))
	}
	if len(seen) < 3 {
		t.Errorf("walked %d report types; there are more", len(seen))
	}
}

// inspect's report reads as the other tools' do: snake_case names, times in
// seconds, and no range for a metric no sample carries.
func TestInspectJSON(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC)
	tr := &fitactivity.Track{Sport: "running"}
	for i := range 61 {
		tr.Samples = append(tr.Samples, fitactivity.Sample{Time: t0.Add(time.Duration(i) * time.Second), HasHeartRate: true, HeartRate: 120 + uint8(i%10)})
	}
	b, err := json.Marshal(jsonReport(inspect.Build(tr)))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["elapsed_s"] != 60.0 || got["samples"] != 61.0 || got["sport"] != "running" {
		t.Errorf("report %s", b)
	}
	for k := range got {
		if strings.ToLower(k) != k {
			t.Errorf("field %q is not snake_case", k)
		}
	}
	for _, m := range got["metrics"].([]any) {
		mm := m.(map[string]any)
		_, hasMin := mm["min"]
		if mm["present"] == 0.0 && hasMin {
			t.Errorf("%v: a range for a metric no sample carries", mm["name"])
		}
		if mm["name"] == inspect.MetricHeartRate && (mm["min"] != 120.0 || mm["coverage"] != 1.0) {
			t.Errorf("heart rate %v", mm)
		}
	}
}
