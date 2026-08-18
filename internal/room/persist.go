package room

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func WriteJSON(path string, session Session) error {
	if err := checkSchemaVersion(session.SchemaVersion); err != nil {
		return err
	}
	b, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".jive-room-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func ReadJSON(path string) (Session, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Session{}, err
	}
	var header struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err = json.Unmarshal(b, &header); err != nil {
		return Session{}, err
	}
	if err = checkSchemaVersion(header.SchemaVersion); err != nil {
		return Session{}, err
	}
	var session Session
	if err = json.Unmarshal(b, &session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func checkSchemaVersion(v string) error {
	const prefix = "jive-room-session/v"
	if !strings.HasPrefix(v, prefix) {
		return fmt.Errorf("unsupported session schema %q (expected %sN)", v, prefix)
	}
	major, err := strconv.Atoi(strings.TrimPrefix(v, prefix))
	if err != nil {
		return fmt.Errorf("unsupported session schema %q (expected %sN)", v, prefix)
	}
	if major != 1 {
		return fmt.Errorf("unsupported session schema %q (this build reads v1 only)", v)
	}
	return nil
}
