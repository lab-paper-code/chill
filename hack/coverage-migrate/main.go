// coverage-migrate is disposable tooling for the d4cbc15b -> 9ccff96e cutover.
// Run from the repository root: GOTMPDIR="$PWD/hack" go run ./hack/coverage-migrate --help
// Read/response close and scratch cleanup failures are returned to the caller.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	evidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type object map[string]json.RawMessage

func decode(b []byte) (object, error) {
	var m object
	err := json.Unmarshal(b, &m)
	if err == nil && m == nil {
		err = errors.New("expected JSON object")
	}
	return m, err
}
func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func str(m object, k string) string     { var s string; _ = json.Unmarshal(m[k], &s); return s }
func child(m object, k string) object   { v, _ := decode(m[k]); return v }
func items(m object, k string) []object { var v []object; _ = json.Unmarshal(m[k], &v); return v }

const (
	domain                           = "chill.dacs.io/"
	profilingRunsResource            = "profilingruns"
	batchEnergyOptimizationsResource = "batchenergyoptimizations"
)

var versions = map[string]string{
	domain + "profiling-run-config.v1alpha17":             domain + "profiling-run-config.v1alpha18",
	domain + "profiling-execution-evidence.v1alpha15":     domain + "profiling-execution-evidence.v1alpha16",
	domain + "profiling-raw-measurement.v1alpha8":         domain + "profiling-raw-measurement.v1alpha9",
	domain + "model-loaded-idle-raw-measurement.v1alpha6": domain + "model-loaded-idle-raw-measurement.v1alpha7",
	domain + "batch-characterization.v1alpha8":            domain + "batch-characterization.v1alpha9",
	domain + "model-loaded-idle-runtime.v1alpha6":         domain + "model-loaded-idle-runtime.v1alpha7",
	domain + "curve-plan.v1alpha9":                        domain + "curve-plan.v1alpha10",
}

func bump(m object) error {
	s := str(m, "schemaVersion")
	if n, ok := versions[s]; ok {
		m["schemaVersion"] = encode(n)
		return nil
	}
	for _, n := range versions {
		if s == n {
			return nil
		}
	}
	return fmt.Errorf("unexpected schema %q", s)
}

// Only deleted declarations, confirmed against the two revisions' type diff.
// Runtime cpuContract and firstfloor.runtime observations are never rewritten.
func transform(b []byte) ([]byte, error) {
	m, err := decode(b)
	if err != nil {
		return nil, err
	}
	schema := str(m, "schemaVersion")
	if strings.Contains(schema, "curve-plan.") {
		shared := child(m, "shared")
		if shared == nil {
			return nil, errors.New("missing shared plan")
		}
		delete(shared, "cpu")
		m["shared"] = encode(shared)
		if err := bump(m); err != nil {
			return nil, err
		}
		return encode(m), nil
	}
	if config := child(m, "runConfig"); config != nil {
		delete(config, "cpu")
		if err := bump(config); err != nil {
			return nil, err
		}
		m["runConfig"] = encode(config)
	}
	if runtime := child(m, "runtime"); runtime != nil {
		derived := child(runtime, "derivedExecutionContract")
		if derived != nil {
			for _, k := range []string{"cpu", "runtimeOptions", "scheduling"} {
				delete(derived, k)
			}
			runtime["derivedExecutionContract"] = encode(derived)
		}
		if err := bump(runtime); err != nil {
			return nil, err
		}
		m["runtime"] = encode(runtime)
	}
	if err := bump(m); err != nil {
		return nil, err
	}
	return encode(m), nil
}

type reference struct {
	Store         string `json:"store"`
	Key           string `json:"key"`
	Size          int64  `json:"size"`
	SchemaVersion string `json:"schemaVersion,omitempty"`
}
type conversion struct {
	Old reference `json:"old"`
	New reference `json:"new"`
}
type statusBackup struct {
	Resource string          `json:"resource"`
	Name     string          `json:"name"`
	UID      string          `json:"uid"`
	Status   json.RawMessage `json:"status"`
	After    json.RawMessage `json:"after"`
}
type backup struct {
	Namespace string         `json:"namespace"`
	CreatedAt time.Time      `json:"createdAt"`
	Statuses  []statusBackup `json:"statuses"`
	Objects   []conversion   `json:"objects"`
}
type migrator struct {
	namespace, endpoint, prefix, backupPath, failPath                   string
	apply                                                               bool
	merges                                                              map[string]string
	store                                                               *stores3.Store
	kube                                                                *http.Client
	host                                                                string
	lists                                                               map[string]object
	mappings                                                            map[string]conversion
	failures                                                            []string
	changes                                                             []statusBackup
	bytes                                                               int64
	rawCount, executionCount, planCount, resultCount, observed, missing int
}

func (m *migrator) get(path string) (b []byte, err error) {
	res, err := m.kube.Get(m.host + path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	b, err = io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", path, res.Status)
	}
	return b, err
}
func (m *migrator) read(r reference) (b []byte, err error) {
	if !strings.HasPrefix(r.Key, m.prefix) {
		return nil, fmt.Errorf("key %s outside prefix %s", r.Key, m.prefix)
	}
	rd, err := m.store.OpenLocation(context.Background(), evidencestore.ObjectLocation{Store: r.Store, Key: r.Key})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rd.Close()) }()
	b, err = io.ReadAll(io.LimitReader(rd, 64<<20+1))
	if len(b) > 64<<20 {
		return nil, errors.New("object exceeds catalog's 64 MiB bound")
	}
	if err == nil && r.Size > 0 && int64(len(b)) != r.Size {
		err = fmt.Errorf("size mismatch: %s expected %d got %d", r.Key, r.Size, len(b))
	}
	return b, err
}
func ref(b []byte) reference    { var r reference; _ = json.Unmarshal(b, &r); return r }
func mapKey(r reference) string { return r.Store + "/" + r.Key }
func (m *migrator) resolve(r reference) reference {
	if c, ok := m.mappings[mapKey(r)]; ok {
		return c.New
	}
	return r
}
func (m *migrator) fail(name string, err error) {
	line := name + ": " + err.Error()
	m.failures = append(m.failures, line)
	fmt.Fprintln(os.Stderr, line)
}
func (m *migrator) convert(r reference) (reference, error) {
	if r.Key == "" {
		return r, errors.New("empty evidence key")
	}
	if c, ok := m.mappings[mapKey(r)]; ok {
		return c.New, nil
	}
	if strings.HasSuffix(r.Key, ".shared-cpu.json") {
		return r, nil
	}
	b, err := m.read(r)
	if err != nil {
		return r, err
	}
	v, err := decode(b)
	if err != nil {
		return r, err
	}
	schema := str(v, "schemaVersion")
	var out []byte
	for _, current := range versions {
		if schema == current {
			return r, nil
		}
	}
	switch {
	case strings.Contains(schema, "profiling-execution-evidence."):
		raw := ref(v["rawMeasurement"])
		next, err := m.convert(raw)
		if err != nil {
			return r, err
		}
		out, err = transform(b)
		if err != nil {
			return r, err
		}
		v, _ = decode(out)
		v["rawMeasurement"] = encode(struct {
			Store string `json:"store"`
			Key   string `json:"key"`
			Size  int64  `json:"size"`
		}{next.Store, next.Key, next.Size})
		out = encode(v)
		m.executionCount++
	case strings.Contains(schema, "raw-measurement."):
		out, err = transform(b)
		if err != nil {
			return r, err
		}
		// Validate every Run, including evidence not referenced by a Complete BEO.
		if strings.Contains(schema, "model-loaded-idle") {
			var raw evidence.IdleRawMeasurement
			err = json.Unmarshal(out, &raw)
			if err == nil {
				err = raw.Validate()
			}
		} else {
			var raw evidence.RawMeasurement
			err = json.Unmarshal(out, &raw)
			if err == nil {
				err = raw.Validate()
			}
		}
		if err != nil {
			return r, err
		}
		runtime := child(v, "runtime")
		cpu := child(runtime, "cpuContract")
		firstfloor := child(child(runtime, "firstfloor"), "runtime")
		lost := []string{}
		if str(cpu, "cgroupCPUMax") == "" {
			lost = append(lost, "runConfig.cpu.limit")
		}
		if cpu["ortIntraOpThreads"] == nil && firstfloor["intraOpThreads"] == nil {
			lost = append(lost, "runConfig.cpu.ortIntraOpThreads")
		}
		if cpu["ortInterOpThreads"] == nil && firstfloor["interOpThreads"] == nil {
			lost = append(lost, "runConfig.cpu.ortInterOpThreads")
		}
		if len(lost) == 0 {
			m.observed++
		} else {
			m.missing++
			fmt.Printf("execution_conditions_declaration_only key=%s removed=%s (no new fields synthesized)\n", r.Key, strings.Join(lost, ","))
		}
		m.rawCount++
	case strings.Contains(schema, "curve-plan."):
		out, err = transform(b)
		if err != nil {
			return r, err
		}
		m.planCount++
	case schema == domain+"batch-energy-optimization-result.v1alpha4":
		plan, err := m.convert(ref(v["curvePlan"]))
		if err != nil {
			return r, err
		}
		v["curvePlan"] = encode(plan)
		trials := items(v, "completedTrials")
		for _, trial := range trials {
			next, err := m.convert(ref(trial["evidence"]))
			if err != nil {
				return r, err
			}
			trial["evidence"] = encode(next)
		}
		v["completedTrials"] = encode(trials)
		out = encode(v)
		m.resultCount++
	default:
		return r, fmt.Errorf("unhandled schema %q", schema)
	}
	header, _ := decode(out)
	next := reference{Store: r.Store, Key: strings.TrimSuffix(r.Key, ".json") + ".shared-cpu.json", Size: int64(len(out)), SchemaVersion: str(header, "schemaVersion")}
	m.mappings[mapKey(r)] = conversion{r, next}
	m.bytes += next.Size
	return next, nil
}
func (m *migrator) converted(c conversion) ([]byte, error) {
	b, err := m.read(c.Old)
	if err != nil {
		return nil, err
	}
	v, _ := decode(b)
	schema := str(v, "schemaVersion")
	if schema == domain+"batch-energy-optimization-result.v1alpha4" {
		v["curvePlan"] = encode(m.resolve(ref(v["curvePlan"])))
		trials := items(v, "completedTrials")
		for _, trial := range trials {
			trial["evidence"] = encode(m.resolve(ref(trial["evidence"])))
		}
		v["completedTrials"] = encode(trials)
		return encode(v), nil
	}
	out, err := transform(b)
	if err != nil {
		return nil, err
	}
	if strings.Contains(schema, "profiling-execution-evidence.") {
		v, _ = decode(out)
		next := m.resolve(ref(v["rawMeasurement"]))
		v["rawMeasurement"] = encode(struct {
			Store string `json:"store"`
			Key   string `json:"key"`
			Size  int64  `json:"size"`
		}{next.Store, next.Key, next.Size})
		return encode(v), nil
	}
	return out, nil
}
func resourcePath(resource, ns string) string {
	if resource == "nodes" || resource == "configmaps" {
		if ns != "" {
			return "/api/v1/namespaces/" + ns + "/" + resource
		}
		return "/api/v1/" + resource
	}
	if ns != "" {
		return "/apis/edge.dacs.io/v1alpha1/namespaces/" + ns + "/" + resource
	}
	return "/apis/edge.dacs.io/v1alpha1/" + resource
}

// For a partial/full rerun, load the old catalog through the original immutable
// roots. This only rewinds the in-memory verification snapshot, never the CRDs.
func (m *migrator) originalStatuses(snapshot map[string]object) error {
	for _, resource := range []string{profilingRunsResource, batchEnergyOptimizationsResource} {
		field := "evidence"
		if resource == batchEnergyOptimizationsResource {
			field = "result"
		}
		objects := items(snapshot[resource], "items")
		for _, obj := range objects {
			status := child(obj, "status")
			current := ref(status[field])
			if !strings.HasSuffix(current.Key, ".shared-cpu.json") {
				continue
			}
			old := reference{Store: current.Store, Key: strings.TrimSuffix(current.Key, ".shared-cpu.json") + ".json"}
			b, err := m.read(old)
			if err != nil {
				return err
			}
			header, err := decode(b)
			if err != nil {
				return err
			}
			old.Size = int64(len(b))
			old.SchemaVersion = str(header, "schemaVersion")
			status[field] = encode(old)
			obj["status"] = encode(status)
		}
		snapshot[resource]["items"] = encode(objects)
	}
	return nil
}
func (m *migrator) snapshot() error {
	for _, resource := range []string{profilingRunsResource, batchEnergyOptimizationsResource, "modelloadedidlebaselines", "modelspecs", "deviceclasses", "datasets", "preprocessors", "nodes", "configmaps"} {
		ns := ""
		switch resource {
		case profilingRunsResource, batchEnergyOptimizationsResource, "modelloadedidlebaselines", "configmaps":
			ns = m.namespace
		}
		b, err := m.get(resourcePath(resource, ns))
		if err != nil {
			return err
		}
		list, err := decode(b)
		if err != nil {
			return err
		}
		m.lists[resource] = list
	}
	return nil
}
func (m *migrator) prepare() {
	for _, resource := range []string{profilingRunsResource, batchEnergyOptimizationsResource} {
		list := m.lists[resource]
		objects := items(list, "items")
		withRef := 0
		for _, obj := range objects {
			meta := child(obj, "metadata")
			status := child(obj, "status")
			field := "evidence"
			if resource != profilingRunsResource {
				field = "result"
			}
			r := ref(status[field])
			if r.Key == "" {
				continue
			}
			withRef++
			if strings.HasSuffix(r.Key, ".shared-cpu.json") || (resource == profilingRunsResource && r.SchemaVersion == domain+"profiling-execution-evidence.v1alpha16") {
				continue
			}
			next, err := m.convert(r)
			if err != nil {
				m.fail(resource+"/"+str(meta, "name"), err)
				continue
			}
			old := append(json.RawMessage(nil), obj["status"]...)
			status[field] = encode(next)
			obj["status"] = encode(status)
			m.changes = append(m.changes, statusBackup{resource, str(meta, "name"), str(meta, "uid"), old, obj["status"]})
		}
		list["items"] = encode(objects)
		fmt.Printf("targets resource=%s total=%d with_evidence=%d\n", resource, len(objects), withRef)
	}
	idle := items(m.lists["modelloadedidlebaselines"], "items")
	results := 0
	for _, obj := range idle {
		if ref(child(obj, "status")["result"]).Key != "" {
			results++
		}
	}
	fmt.Printf("MLIB total=%d result_references=%d catalog_coverage_consumers=0 (catalog uses BEO completedTrials idle Runs; all Run evidence included)\n", len(idle), results)
}

// The bridge is an in-memory overlay: raw bytes are fetched and discarded one
// object at a time. Only CRDs and old/new identities survive a read.
func (m *migrator) bridge(converted bool) *httptest.Server {
	byNew := make(map[string]conversion, len(m.mappings))
	for _, c := range m.mappings {
		byNew[mapKey(c.New)] = c
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "read only", 405)
			return
		}
		if r.URL.Path == "/object" {
			key := r.URL.Query().Get("key")
			store := r.URL.Query().Get("store")
			var b []byte
			var err error
			if converted {
				if c, found := byNew[store+"/"+key]; found {
					b, err = m.converted(c)
				} else {
					b, err = m.read(reference{Store: store, Key: key})
				}
			} else {
				b, err = m.read(reference{Store: store, Key: key})
			}
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			_, _ = w.Write(b)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/kube")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		w.Header().Set("Content-Type", "application/json")
		// Discovery is read from the live API; actual objects always use the frozen snapshot.
		resourceIndex := 2
		if strings.HasPrefix(path, "/apis/") {
			resourceIndex = 3
		}
		if len(parts) > resourceIndex && parts[resourceIndex] == "namespaces" {
			resourceIndex += 2
		}
		if len(parts) > resourceIndex {
			resource := parts[resourceIndex]
			if list, ok := m.lists[resource]; ok {
				if len(parts) == resourceIndex+1 {
					_, _ = w.Write(encode(list))
					return
				}
				name := parts[resourceIndex+1]
				for _, obj := range items(list, "items") {
					if str(child(obj, "metadata"), "name") == name {
						_, _ = w.Write(encode(obj))
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				_, _ = w.Write(encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404}))
				return
			}
		}
		b, err := m.get(path)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
}

type verification struct {
	Usable   int                        `json:"usable"`
	Batches  map[string]json.RawMessage `json:"batches"`
	Issues   []json.RawMessage          `json:"issues"`
	Digest   []object                   `json:"digest"`
	Retained int                        `json:"retained"`
}

func verifier(root, revision, scratch string) (string, error) {
	dir := filepath.Join(scratch, revision)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	archive := exec.Command("git", "archive", revision)
	rd, err := archive.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := archive.Start(); err != nil {
		return "", err
	}
	tr := tar.NewReader(rd)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		path := filepath.Join(dir, h.Name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		f, err := os.Create(path)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(f, tr)
		_ = f.Close()
		if err != nil {
			return "", err
		}
	}
	if err := archive.Wait(); err != nil {
		return "", err
	}
	template, target, buildPath := "verify.go.txt", "cmd/chill-query/covmig_verify.go", "./cmd/chill-query"
	if revision == "d4cbc15b" {
		template, target, buildPath = "before.go.txt", "hack/covmig-before/main.go", "./hack/covmig-before"
	}
	harness, err := os.ReadFile(filepath.Join(root, "hack/coverage-migrate", template))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, target)), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, target), harness, 0600); err != nil {
		return "", err
	}
	binary := filepath.Join(scratch, "verify-"+revision)
	build := exec.Command("go", "build", "-o", binary, buildPath)
	build.Dir = dir
	build.Env = append(os.Environ(), "GOTMPDIR="+scratch)
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return "", err
	}
	return binary, nil
}
func (m *migrator) verify(binary string, converted bool) (verification, error) {
	bridge := m.bridge(converted)
	defer bridge.Close()
	c := exec.Command(binary)
	c.Env = append(os.Environ(), "CHILL_COVMIG_VERIFY="+bridge.URL, "CHILL_COVMIG_NAMESPACE="+m.namespace)
	c.Stderr = os.Stderr
	b, err := c.Output()
	var v verification
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func equalJSON(a, b []byte) bool {
	var x, y any
	da := json.NewDecoder(bytes.NewReader(a))
	da.UseNumber()
	db := json.NewDecoder(bytes.NewReader(b))
	db.UseNumber()
	return da.Decode(&x) == nil && db.Decode(&y) == nil && reflect.DeepEqual(x, y)
}
func (m *migrator) saveBackup() error {
	if existing, err := os.ReadFile(m.backupPath); err == nil {
		var saved backup
		if err = json.Unmarshal(existing, &saved); err != nil {
			return err
		}
		if saved.Namespace != m.namespace {
			return errors.New("existing backup namespace differs")
		}
		covered := map[string]statusBackup{}
		for _, s := range saved.Statuses {
			covered[s.Resource+"/"+s.Name] = s
		}
		for _, s := range m.changes {
			old, ok := covered[s.Resource+"/"+s.Name]
			if !ok || old.UID != s.UID || !equalJSON(old.Status, s.Status) || !equalJSON(old.After, s.After) {
				return errors.New("existing backup does not cover unchanged migration targets")
			}
		}
		locations := map[string]conversion{}
		for _, c := range saved.Objects {
			locations[mapKey(c.Old)] = c
		}
		for _, c := range m.mappings {
			old, ok := locations[mapKey(c.Old)]
			if !ok || old != c {
				return errors.New("existing backup has a different old/new key mapping")
			}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	objects := make([]conversion, 0, len(m.mappings))
	for _, c := range m.mappings {
		objects = append(objects, c)
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Old.Key < objects[j].Old.Key })
	b := encode(backup{m.namespace, time.Now().UTC(), m.changes, objects})
	f, err := os.OpenFile(m.backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func (m *migrator) patch(s statusBackup, status json.RawMessage) (err error) {
	path := resourcePath(s.Resource, m.namespace) + "/" + s.Name
	b, err := m.get(path)
	if err != nil {
		return err
	}
	obj, err := decode(b)
	if err != nil {
		return err
	}
	meta := child(obj, "metadata")
	if str(meta, "uid") != s.UID {
		return errors.New("object UID changed")
	}
	expected := s.Status
	if bytes.Equal(status, s.Status) {
		expected = s.After
	}
	if !equalJSON(obj["status"], expected) {
		if equalJSON(obj["status"], status) {
			return nil
		}
		return errors.New("status changed since backup; refusing overwrite")
	}
	patch := encode([]map[string]any{{"op": "test", "path": "/metadata/resourceVersion", "value": str(meta, "resourceVersion")}, {"op": "replace", "path": "/status", "value": status}})
	req, err := http.NewRequest("PATCH", m.host+path+"/status", bytes.NewReader(patch))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json-patch+json")
	res, err := m.kube.Do(req)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, res.Body.Close()) }()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("patch: %s %s", res.Status, b)
	}
	return nil
}
func (m *migrator) writeObjects() {
	objects := make([]conversion, 0, len(m.mappings))
	for _, c := range m.mappings {
		objects = append(objects, c)
	}
	rank := func(c conversion) int {
		switch {
		case strings.Contains(c.New.SchemaVersion, "raw-measurement"):
			return 0
		case strings.Contains(c.New.SchemaVersion, "execution-evidence"):
			return 1
		case strings.Contains(c.New.SchemaVersion, "curve-plan"):
			return 2
		default:
			return 3
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		a, b := rank(objects[i]), rank(objects[j])
		if a != b {
			return a < b
		}
		return objects[i].New.Key < objects[j].New.Key
	})
	failed := map[string]bool{}
	for _, c := range objects {
		b, err := m.converted(c)
		if err == nil {
			value, _ := decode(b)
			dependencies := []reference{ref(value["rawMeasurement"]), ref(value["curvePlan"])}
			for _, trial := range items(value, "completedTrials") {
				dependencies = append(dependencies, ref(trial["evidence"]))
			}
			for _, dependency := range dependencies {
				if failed[dependency.Key] {
					err = fmt.Errorf("dependency %s failed", dependency.Key)
					break
				}
			}
		}
		if err == nil {
			existing, readErr := m.read(c.New)
			if readErr == nil {
				if !bytes.Equal(existing, b) {
					err = errors.New("existing new key differs")
				}
			} else if errors.Is(readErr, evidencestore.ErrNotFound) {
				var receipt evidencestore.Receipt
				receipt, err = m.store.PutImmutable(context.Background(), evidencestore.ObjectIdentity{Store: c.New.Store, Key: c.New.Key, Size: c.New.Size}, bytes.NewReader(b))
				if err == nil && receipt.Outcome == evidencestore.CreateOutcomeExisting {
					existing, err = m.read(c.New)
					if err == nil && !bytes.Equal(existing, b) {
						err = errors.New("concurrent existing new key differs")
					}
				}
			} else {
				err = readErr
			}
		}
		if err != nil {
			failed[c.New.Key] = true
			m.fail(c.New.Key, err)
		}
	}
	// Never publish a status referencing an object whose write failed.
	for _, s := range m.changes {
		status, _ := decode(s.After)
		root := ref(status["result"])
		if s.Resource == profilingRunsResource {
			root = ref(status["evidence"])
		}
		if s.Resource == batchEnergyOptimizationsResource {
			old, _ := decode(s.Status)
			if c, ok := m.mappings[mapKey(ref(old["result"]))]; ok {
				b, err := m.converted(c)
				if err != nil {
					m.fail(s.Resource+"/"+s.Name, err)
					continue
				}
				value, _ := decode(b)
				for _, trial := range items(value, "completedTrials") {
					if failed[ref(trial["evidence"]).Key] {
						failed[root.Key] = true
						break
					}
				}
			}
		}
		if failed[root.Key] {
			m.fail(s.Resource+"/"+s.Name, errors.New("evidence write failed; status unchanged"))
			continue
		}
		if err := m.patch(s, s.After); err != nil {
			failed[root.Key] = true
			m.fail(s.Resource+"/"+s.Name, err)
		}
	}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAILED:", err)
		os.Exit(1)
	}
}
func run() (err error) {
	var namespace, endpoint, prefix, backupPath, failPath, restore, baseline string
	var apply, dry bool
	var free, capacity int64
	flag.StringVar(&namespace, "namespace", "chill-system", "Namespace of the current deployment and measurements")
	flag.StringVar(&endpoint, "store-endpoint", "", "S3 endpoint override (e.g. a read-only port-forward); default: deployment args")
	flag.StringVar(&prefix, "store-prefix", "", "Restrict keys to this prefix")
	flag.StringVar(&backupPath, "backup", "coverage-migration-status.json", "Exclusive-create local status backup including old/new key mapping")
	flag.StringVar(&failPath, "failures", "coverage-migration-failures.txt", "Failed object list, including validation failures")
	flag.StringVar(&restore, "restore", "", "Restore statuses from this backup; requires --apply to write")
	flag.StringVar(&baseline, "baseline", filepath.Join(os.Getenv("HOME"), ".cache/advisor1-profile-baseline/base/digest.json"), "Captured live profile digest (coverage.json must be alongside it)")
	flag.BoolVar(&apply, "apply", false, "Write immutable objects then patch statuses; only in a maintenance window")
	flag.BoolVar(&dry, "dry-run", true, "Read-only (default); --apply selects write mode")
	flag.Int64Var(&free, "store-free-bytes", 41900000000, "Observed available store bytes (refresh this observation before apply)")
	flag.Int64Var(&capacity, "store-capacity-bytes", 128000000000, "Observed store capacity bytes")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, `One-shot coverage migration: d4cbc15b -> 9ccff96e, profile identity unchanged.
Usage: GOTMPDIR="$PWD/hack" go run ./hack/coverage-migrate --namespace chill-system --store-endpoint http://127.0.0.1:19000
Apply: add --apply --backup /private/path/status.json; restore: --restore /private/path/status.json --apply.
Run kubectl -n chill-system port-forward svc/chill-evidence-store 19000:9000 if needed.
Credentials, logical store, bucket and region come from the current serving deployment/Secret.
MAINTENANCE WINDOW REQUIRED immediately before or after helm upgrade: old server
cannot read converted evidence; new server cannot read originals. Quiesce writers.
Original write-once keys stay intact; only CRD statuses need backup. Restore statuses
before rolling back the server. No spec or campaignTrial changes are made.
Decision A permits exactly the captured 259 -> 242 selection: independently group
the original conditions without CPU and retain each latest measurement. The exact
17 non-latest IDs must match actual omissions; any other profile loss blocks apply.
The baseline directory must include digest.json, profiles.json and coverage.json.
Raw objects are streamed one at a time, never cached on local disk. Verification
builds the old catalog and new Query/catalog under hack/; old coverage is the
captured live API baseline. All temporary source/binaries are removed on exit.
Store capacity flags are observations, not discovery; refresh them before apply.
A predicted usage >=86% fails apply (the store previously failed at 86%).`)
		flag.PrintDefaults()
	}
	flag.Parse()
	_ = dry
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return err
	}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return err
	}
	m := &migrator{namespace: namespace, endpoint: endpoint, prefix: prefix, backupPath: backupPath, failPath: failPath, apply: apply, kube: httpClient, host: strings.TrimRight(config.Host, "/"), lists: map[string]object{}, mappings: map[string]conversion{}}
	defer func() {
		text := strings.Join(m.failures, "\n")
		if text != "" {
			text += "\n"
		}
		_ = os.WriteFile(failPath, []byte(text), 0600)
	}()
	if restore != "" {
		return m.restoreStatuses(restore)
	}
	if err := m.configureStore(); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(filepath.Join(root, "hack"), ".coverage-migrate-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(scratch)) }()
	oldBinary, err := verifier(root, "d4cbc15b", scratch)
	if err != nil {
		return err
	}
	newBinary, err := verifier(root, "9ccff96e", scratch)
	if err != nil {
		return err
	}
	if err = m.snapshot(); err != nil {
		return err
	}
	// Keep only the small CRD snapshot for the baseline; no scientific raw payloads.
	original := m.lists
	m.lists = map[string]object{}
	for k, v := range original {
		m.lists[k], _ = decode(encode(v))
	}
	m.prepare()
	converted := m.lists
	fmt.Printf("changes remove=runConfig.cpu,shared.cpu,runtime.derivedExecutionContract.{cpu,runtimeOptions,scheduling}; bump=runConfig,execution,raw,runtime,curve-plan; references=rawMeasurement,curvePlan,completedTrials,status\n")
	fmt.Printf("objects raw=%d execution=%d curve_plan=%d result=%d total=%d new_bytes=%d available_bytes=%d projected_usage_percent=%.2f fits=%t\n", m.rawCount, m.executionCount, m.planCount, m.resultCount, len(m.mappings), m.bytes, free, 100*float64(capacity-free+m.bytes)/float64(capacity), m.bytes <= free)
	fmt.Printf("execution_conditions observed_quota_and_threads=%d declaration_only=%d; runtime cpuContract/firstfloor observations retained; removed declarations remain at original keys\n", m.observed, m.missing)
	m.lists = original
	if err := m.originalStatuses(original); err != nil {
		return fmt.Errorf("original verification roots: %w", err)
	}
	before, err := m.verify(oldBinary, false)
	if err != nil {
		return fmt.Errorf("old catalog/coverage: %w", err)
	}
	before.Usable, err = baselineCoverage(baseline)
	if err != nil {
		return fmt.Errorf("captured live coverage: %w", err)
	}
	m.lists = converted
	after, err := m.verify(newBinary, true)
	if err != nil {
		return fmt.Errorf("new catalog/coverage: %w", err)
	}
	if err := m.compareBaseline(baseline, after); err != nil {
		return fmt.Errorf("captured live baseline: %w", err)
	}
	mismatches, missingProfiles := m.compareCatalogs(before, after)
	var disk int64
	_ = filepath.Walk(scratch, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			disk += info.Size()
		}
		return nil
	})
	fmt.Printf("verification profiles_before=%d profiles_after=%d approved_nonlatest_profiles=%d profile_id_changes=0 usable_before=%d usable_after=%d numeric_mismatches=%d failures=%d raw_local_bytes=0 local_scratch_bytes=%d (removed on exit)\n", len(before.Batches), len(after.Batches), missingProfiles, before.Usable, after.Usable, mismatches, len(m.failures), disk)
	if len(m.failures) > 0 {
		return fmt.Errorf("%d failures; see %s", len(m.failures), failPath)
	}
	if m.bytes > free {
		return errors.New("converted objects exceed store free space")
	}
	if apply && len(m.changes) > 0 {
		if float64(capacity-free+m.bytes)/float64(capacity) >= 0.86 {
			return errors.New("projected store usage reaches prior 86% failure threshold")
		}
		if err := m.saveBackup(); err != nil {
			return err
		}
		m.writeObjects()
		if len(m.failures) > 0 {
			return fmt.Errorf("%d apply failures; see %s", len(m.failures), failPath)
		}
	}
	fmt.Printf("dry_run=%t statuses_to_update=%d failures=%d backup=%s\n", !apply, len(m.changes), len(m.failures), backupPath)
	return nil
}

func (m *migrator) restoreStatuses(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var saved backup
	if err = json.Unmarshal(b, &saved); err != nil {
		return err
	}
	if saved.Namespace != m.namespace {
		return errors.New("backup namespace mismatch")
	}
	fmt.Printf("restore statuses=%d dry_run=%t\n", len(saved.Statuses), !m.apply)
	if m.apply {
		for i := len(saved.Statuses) - 1; i >= 0; i-- {
			s := saved.Statuses[i]
			if err := m.patch(s, s.Status); err != nil {
				m.fail(s.Resource+"/"+s.Name, err)
			}
		}
	}
	if len(m.failures) > 0 {
		return errors.New("restore failures")
	}
	return nil
}

func (m *migrator) compareCatalogs(before, after verification) (mismatches, missingProfiles int) {
	for id, b := range before.Batches {
		a, ok := after.Batches[id]
		if !ok {
			missingProfiles++
			if m.merges[id] == "" {
				m.fail("profile/"+id, errors.New("profile omitted by new catalog selection"))
			}
			continue
		}
		if !equalJSON(b, a) {
			mismatches++
			m.fail("profile/"+id, errors.New("batch duration samples/energy changed"))
		}
	}
	for id := range after.Batches {
		if _, ok := before.Batches[id]; !ok {
			m.fail("profile/"+id, errors.New("unexpected new profile"))
		}
	}
	if before.Usable != 132 || after.Usable != before.Usable {
		m.fail("coverage", fmt.Errorf("usable before=%d after=%d expected=132", before.Usable, after.Usable))
	}
	if !equalJSON(encode(before.Issues), encode(after.Issues)) {
		m.fail("catalog", errors.New("catalog issues differ"))
	}
	if before.Retained != after.Retained {
		m.fail("catalog", fmt.Errorf("retained measurement facts changed: %d -> %d", before.Retained, after.Retained))
	}
	return mismatches, missingProfiles
}

func (m *migrator) configureStore() error {
	deployment, err := m.get("/apis/apps/v1/namespaces/" + m.namespace + "/deployments/chill-serving-entrypoint")
	if err != nil {
		return err
	}
	dep, _ := decode(deployment)
	containers := items(child(child(child(dep, "spec"), "template"), "spec"), "containers")
	args := map[string]string{}
	for _, container := range containers {
		var aa []string
		_ = json.Unmarshal(container["args"], &aa)
		for _, arg := range aa {
			p := strings.SplitN(strings.TrimPrefix(arg, "--"), "=", 2)
			if len(p) == 2 {
				args[p[0]] = p[1]
			}
		}
	}
	endpoint := m.endpoint
	if endpoint == "" {
		endpoint = args["store-endpoint"]
	}
	secretBytes, err := m.get("/api/v1/namespaces/" + m.namespace + "/secrets/" + args["store-credentials-secret"])
	if err != nil {
		return err
	}
	var secret struct {
		Data map[string][]byte `json:"data"`
	}
	if err = json.Unmarshal(secretBytes, &secret); err != nil {
		return err
	}
	tempDir, err := filepath.Abs("hack")
	if err != nil {
		return err
	}
	m.store, err = stores3.New(stores3.Config{LogicalStore: args["store-name"], Bucket: args["store-bucket"], Region: args["store-region"], Endpoint: endpoint, AccessKeyID: string(secret.Data["access-key"]), SecretAccessKey: string(secret.Data["secret-key"]), TempDir: tempDir})
	return err
}
