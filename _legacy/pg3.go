package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/bytedance/sonic"
	"github.com/k0kubun/pp"
	"sales-way.com/server/rel"
	"sales-way.com/server/sw"
)

func setupPg3Routes(srv *sw.SwServer) error {
	srv.LogInfo("setting up pg3 routes")
	srv.Router.Post("/rel2", func(w http.ResponseWriter, r *http.Request) {
		if !hasBody(r) {
			http.Error(w, "request body required", http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		ops, err := rel.ParseFromBytes(srv.Tables, srv.Functions, body)
		if err != nil {
			_printStackTrace(w, err, string(body))
			return
		}

		subscope := srv.RootScope.Child()
		resolveCtx := rel.NewResolveContext(srv.Tables, srv.Functions, subscope)
		resolveCtx.AddBody()

		if err := ops.Resolve(resolveCtx); err != nil {
			_printStackTrace(w, err, string(body))
			return
		}

		response := make([]any, 0, len(ops))
		for _, op := range ops {
			item := map[string]any{
				"relation": op.Query.Relation.TableKey(),
				"readonly": op.Query.IsReadOnly(),
			}

			if op.Data != nil {
				var data any
				raw, _ := op.Data.Raw()
				if err := json.Unmarshal([]byte(raw), &data); err == nil {
					item["data_count"] = dataLength(data)
				}
				if flats, err := op.FlattenJsonData(body); err != nil {
					_printStackTrace(w, err, string(body))
					return
				} else {
					item["flat_rows"] = len(flats)
				}
			}

			response = append(response, item)
		}

		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		sonic.ConfigDefault.NewEncoder(w).Encode(response)
		pp.Println(ops)
	})

	return nil
}

func dataLength(data any) int {
	switch v := data.(type) {
	case []any:
		return len(v)
	default:
		return 1
	}
}
