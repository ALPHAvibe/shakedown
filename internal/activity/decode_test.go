package activity

import (
	"encoding/json"
	"io"
	"net/http"
)

func decode(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst)
}
