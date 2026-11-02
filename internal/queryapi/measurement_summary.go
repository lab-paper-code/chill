package queryapi

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// MeasurementSummaryJSON shares repeated display evidence within one response.
// Values are emitted before their references, so readers can restore them in one
// pass. RawMessage keeps retained integer evidence exact (including uint64).
func MeasurementSummaryJSON(payload any, omitPreparations bool) (json.RawMessage, error) {
	root, err := measurementSummaryRoot(payload, omitPreparations)
	if err != nil {
		return nil, err
	}
	values := []json.RawMessage{}
	indices := map[string]int{}
	shapes := [][]string{}
	shapeIndices := map[string]int{}
	var project func(json.RawMessage, string) (json.RawMessage, error)
	project = func(value json.RawMessage, field string) (json.RawMessage, error) {
		if len(value) == 0 {
			return value, nil
		}
		reserved := false
		if value[0] == '"' {
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return nil, err
			}
			reserved = measurementSummaryReference(text)
		}
		switch value[0] {
		case '{':
			var object map[string]json.RawMessage
			if err := json.Unmarshal(value, &object); err != nil {
				return nil, err
			}
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				next, err := project(object[key], key)
				if err != nil {
					return nil, err
				}
				object[key] = next
			}
			shapeJSON, shapeErr := json.Marshal(keys)
			if shapeErr != nil {
				return nil, shapeErr
			}
			_, reservedObject := object["$o"]
			if field != "$root" && (len(shapeJSON) > 32 || reservedObject) {
				index, exists := shapeIndices[string(shapeJSON)]
				if !exists {
					index = len(shapes)
					shapeIndices[string(shapeJSON)] = index
					shapes = append(shapes, keys)
				}
				fields := make([]json.RawMessage, 1, len(keys)+1)
				fields[0] = json.RawMessage(strconv.Itoa(index))
				for _, key := range keys {
					fields = append(fields, object[key])
				}
				value, err = json.Marshal(struct {
					Fields []json.RawMessage `json:"$o"`
				}{fields})
			} else {
				value, err = json.Marshal(object)
			}
		case '[':
			var items []json.RawMessage
			if err := json.Unmarshal(value, &items); err != nil {
				return nil, err
			}
			for i := range items {
				next, err := project(items[i], "")
				if err != nil {
					return nil, err
				}
				items[i] = next
			}
			value, err = json.Marshal(items)
		}
		if err != nil {
			return nil, err
		}
		if field != "$root" && (len(value) >= 24 || reserved) {
			index, exists := indices[string(value)]
			if !exists {
				index = len(values)
				indices[string(value)] = index
				values = append(values, value)
			}
			return json.Marshal("~" + strconv.Itoa(index))
		}
		return value, nil
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	encoded, err = project(encoded, "$root")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &root); err != nil {
		return nil, err
	}
	root["readProjection"], err = json.Marshal(struct {
		Version string            `json:"version"`
		Values  []json.RawMessage `json:"values"`
		Shapes  [][]string        `json:"shapes"`
	}{"measurement-summary.v1", values, shapes})
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func measurementSummaryReference(text string) bool {
	return len(text) > 1 && text[0] == '~' && strings.Trim(text[1:], "0123456789") == ""
}

func measurementSummaryRoot(payload any, omitPreparations bool) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if omitPreparations {
		delete(root, "preparations")
	}
	if _, coverage := root["cells"]; coverage {
		delete(root, "entities")
		delete(root, "groups")
	}
	if rawItems, list := root["items"]; list {
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(rawItems, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(item["metadata"], &metadata); err != nil {
				return nil, err
			}
			delete(metadata, "managedFields")
			delete(metadata, "annotations")
			item["metadata"], err = json.Marshal(metadata)
			if err != nil {
				return nil, err
			}
		}
		root["items"], err = json.Marshal(items)
		if err != nil {
			return nil, err
		}
	}
	return root, nil
}
