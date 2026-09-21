package config

import (
	"bytes"
	"encoding/json"
	"errors"
)

// WithUpdateChannel edits only the update subscription, preserving the user's
// configuration and existing update state/staging paths. A missing section is
// materialized from distribution defaults, never from new state directories.
func WithUpdateChannel(data []byte, channel string, defaults *UpdateConfig) ([]byte, error) {
	if channel != "alpha" && channel != "beta" && channel != "stable" {
		return nil, errors.New("update channel must be alpha, beta or stable")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("configuration must be a JSON object")
	}
	var file jsonConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	view, err := normalizeClientView(file)
	if err != nil {
		return nil, err
	}
	if view.UpdateConfigured && view.Update == nil {
		return nil, errors.New("updates are explicitly disabled; choosing a channel will not enable them")
	}
	var update map[string]json.RawMessage
	if view.UpdateConfigured {
		if file.Update.Channel == channel {
			return data, nil
		}
		if err := json.Unmarshal(fields["update"], &update); err != nil {
			return nil, err
		}
	} else {
		if defaults == nil {
			return nil, errors.New("this build has no update distribution defaults")
		}
		validated, err := NewUpdateConfig(defaults.RepoURL, channel, defaults.Component, defaults.Platform, defaults.StatePath, defaults.StageRoot, defaults.SVNProgram)
		if err != nil {
			return nil, err
		}
		validated.SSH = defaults.SSH
		if err := validated.ValidateTransport(); err != nil {
			return nil, err
		}
		values := map[string]any{
			"enabled": true, "repo_url": validated.RepoURL, "channel": channel,
			"component": validated.Component, "platform": validated.Platform,
			"state_path": validated.StatePath, "stage_root": validated.StageRoot,
			"svn_program": validated.SVNProgram,
		}
		if validated.SSH != nil {
			values["ssh"] = validated.SSH
		}
		raw, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
	}
	update["channel"], _ = json.Marshal(channel)
	fields["update"], err = json.Marshal(update)
	if err != nil {
		return nil, err
	}
	result, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}
