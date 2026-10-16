package runs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// ErrUnsupportedFormat prevents recovery from replacing a newer store. Its
// original bytes must be read by a compatible binary rather than guessed at.
var ErrUnsupportedFormat = errors.New("unsupported run store format")
var featureIDPattern = regexp.MustCompile(`^FTR-[0-9]{4}$`)

// decodeDocument is shared by ordinary reads and explicit recovery, so recovery
// cannot mistake an incomplete object for an intentionally empty store.
func decodeDocument(raw []byte) (*document, error) {
	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, err
	}
	if header == nil {
		return nil, fmt.Errorf("expected a run store object")
	}
	encodedVersion, ok := header["version"]
	if !ok || bytes.Equal(bytes.TrimSpace(encodedVersion), []byte("null")) {
		return nil, fmt.Errorf("run store omitted its version")
	}
	var storedVersion int
	if err := json.Unmarshal(encodedVersion, &storedVersion); err != nil {
		return nil, fmt.Errorf("invalid run store version: %w", err)
	}
	if storedVersion > version || storedVersion < 1 {
		return nil, fmt.Errorf("%w: version %d requires a compatible hvb (this binary supports 1 through %d)", ErrUnsupportedFormat, storedVersion, version)
	}
	if _, ok := header["runs"]; !ok {
		return nil, fmt.Errorf("run store omitted its runs array")
	}
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if err := validateDocument(&doc); err != nil {
		return nil, err
	}
	doc.Version = version
	return &doc, nil
}

func validateDocument(doc *document) error {
	seen := map[string]bool{}
	for _, run := range doc.Runs {
		if run == nil || run.ID == "" || seen[run.ID] {
			return fmt.Errorf("invalid or duplicate run records")
		}
		seen[run.ID] = true
		if !featureIDPattern.MatchString(run.FeatureID) || !run.Status.Valid() || run.StartedAt.IsZero() {
			return fmt.Errorf("run %s has incomplete feature, lifecycle, or start metadata", run.ID)
		}
		switch run.State {
		case Queued, Running, Awaiting, Succeeded, Failed, Cancelled:
		default:
			return fmt.Errorf("run %s has an unknown state", run.ID)
		}
	}
	return nil
}
