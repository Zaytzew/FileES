package commit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

// RecoveryCacheProof removes queue entry order, outer field order and JSON
// whitespace from the proof. RawMessage retains every field (including future fields),
// numeric representation, duplicate entry and receipt order. It also accepts
// legacy queues, so their first deterministic rewrite cannot invalidate a plan.
// Unsupported or ambiguous input returns an error; callers can bind its exact
// bytes instead, never omit that file from the proof.
func RecoveryCacheProof(data []byte) ([]byte, error) {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return data, nil
	}
	if bytes.HasPrefix(data, []byte("[")) {
		return recoveryQueueProof(data)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("commit cache is not an object or queue")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid commit cache field")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("duplicate commit cache field")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if err := d.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, errors.New("trailing commit cache data")
	}
	var schema string
	if json.Unmarshal(fields["schema"], &schema) != nil || schema != intentCacheSchema {
		return nil, errors.New("unsupported commit cache schema")
	}
	queue, err := recoveryQueueProof(fields["entries"])
	if err != nil {
		return nil, err
	}
	fields["entries"] = queue
	return json.Marshal(fields)
}

func recoveryQueueProof(data []byte) ([]byte, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	for i, entry := range entries {
		var compact bytes.Buffer
		if err := json.Compact(&compact, entry); err != nil {
			return nil, err
		}
		entries[i] = compact.Bytes()
	}
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i], entries[j]) < 0 })
	return json.Marshal(entries)
}
