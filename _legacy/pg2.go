package main

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/valyala/bytebufferpool"
	"sales-way.com/server/pg"
	"sales-way.com/server/query"
	"sales-way.com/server/sw"
	"sales-way.com/server/templates"
)

//go:embed web/static/scripts/model.ts
var modelTs string

type SqlRequest struct {
	sql     string
	args    [][]any
	outputs bool
}

// runJsonArraySql runs a sql query and writes the result as json to the response.
func runJsonArraySql(srv *sw.SwServer, w http.ResponseWriter, r *http.Request, requests []SqlRequest) {

	conn, err := srv.Pool.Acquire(r.Context())
	if err != nil {
		http.Error(w, "Error acquiring pool: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Release()

	role, _ := jwtGetRoleFromRequest(r)

	trans, err := conn.Begin(r.Context())
	if err != nil {
		http.Error(w, "Error starting transaction: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		if err == nil {
			if err = trans.Commit(r.Context()); err != nil {
				srv.LogError(err.Error())
			}
		} else {
			if err = trans.Rollback(r.Context()); err != nil {
				srv.LogError(err.Error())
			}
		}
	}()

	if _, err = trans.Exec(r.Context(), `SET LOCAL ROLE "`+role+`"; SET LOCAL "app.current_role" = '`+role+`';`); err != nil {
		http.Error(w, "Error setting role: "+err.Error()+"\n", http.StatusInternalServerError)
		return
	}

	if DEBUG_ENABLED {
		w.Header().Add("X-Db-Role", role)
	}

	for _, request := range requests {

		if request.args != nil {
			if _, err := trans.CopyFrom(r.Context(), pgx.Identifier{"__flat_json_temp"}, []string{"___node_id", "___parent_id", "___selection_index", "json"}, pgx.CopyFromRows(request.args)); err != nil {
				srv.LogError(err.Error())
				_printStackTrace(w, err, request.sql)
				http.Error(w, "Error executing query: "+err.Error()+"\n"+request.sql, http.StatusInternalServerError)
				return
			} else {
				// pp.Println("copied", copied)
				// pp.Println("request.args", request.args)
			}
			continue
		}

		var rows pgx.Rows

		rows, err = trans.Query(r.Context(), request.sql)

		if err != nil {
			srv.LogError(err.Error())
			_printStackTrace(w, err, request.sql)
			//
			http.Error(w, "Error executing query: "+err.Error()+"\n"+request.sql, http.StatusInternalServerError)
			return
		}

		buf := bytebufferpool.Get()
		defer bytebufferpool.Put(buf)
		// There should be only one row.
		for rows.Next() {
			var raw = rows.RawValues()
			if request.outputs {
				if len(raw) == 0 {
					buf.WriteString("null")
				} else {
					buf.Write(raw[0])
				}
			}
		}

		if err := rows.Err(); err != nil {
			if pgxErr, ok := err.(*pgconn.PgError); ok {
				if pgxErr.Code == "42501" {
					if role == DB_ANON_ROLE {
						w.WriteHeader(http.StatusUnauthorized)
					} else {
						w.WriteHeader(http.StatusForbidden)
					}
					w.Write([]byte(pgxErr.Message + " - " + request.sql))
					return
				}
			}

			http.Error(w, "Error executing query: "+err.Error()+"\n"+request.sql, http.StatusInternalServerError)
			return
		}

		w.Header().Add("Content-Type", "application/json;charset=utf-8")
		w.Write(buf.Bytes())

		rows.Close()
	}

}

func isMultipart(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/")
}

// getTinySQLRequest returns the request string from the url.
func getTinySQLRequest(r *http.Request, is_multipart bool) string {
	query, _ := url.QueryUnescape(r.Header.Get("X-Query"))
	from_url := false

	if query == "" && is_multipart {
		query = r.FormValue("query")
	}

	if query == "" {
		// X-Request should be used for POST requests.
		test_query := chi.URLParam(r, "*")
		if len(test_query) > 0 {
			query = test_query
			from_url = true
		} else {
			return ""
		}
	}

	// There could be a ? in the request, but we don't treat it as a query parameter.
	if r.URL.RawQuery != "" {
		query += "?" + r.URL.RawQuery
	}

	if from_url {
		// If the request is from the url, we need to unescape the query string.
		query, _ = url.QueryUnescape(query)
	}

	query = strings.ReplaceAll(query, "\\n", "\n")
	query = strings.ReplaceAll(query, "\\\\", "\\")

	return query
}

func hasBody(req *http.Request) bool {
	if req.Body == nil {
		return false
	}
	if req.ContentLength > 0 {
		return true
	}
	if req.ContentLength == 0 && !isChunked(req) {
		return false
	}
	// Handle Transfer-Encoding: chunked (common for streaming clients)
	return true
}

func isChunked(req *http.Request) bool {
	return len(req.TransferEncoding) > 0 && req.TransferEncoding[0] == "chunked"
}

func SetupPG2Routes(srv *sw.SwServer, router chi.Router) {

	var relqlHandler = func(w http.ResponseWriter, r *http.Request) {
		savepoints := 0
		is_multipart := isMultipart(r)
		if is_multipart {
			r.ParseMultipartForm(32 << 20) // 32MB. FIXME : should be configurable
		}

		request := getTinySQLRequest(r, is_multipart)
		ops, err := query.Parse2(srv.Tables, []byte(request))
		if err != nil {
			_printStackTrace(w, err, "")
			return
		}

		os.Stdout.WriteString(request + "\n")

		subscope := srv.RootScope.Child()
		resolveCtx := query.NewResolveContext(srv.Tables, srv.Functions, subscope)
		resolveCtx.AddBody()

		requests := make([]SqlRequest, 0)

		body := []byte{}

		if is_multipart {
			body = []byte(r.FormValue("payload"))
		}

		if len(body) == 0 && hasBody(r) {
			body, err = io.ReadAll(r.Body)

			if err != nil {
				_printStackTrace(w, err, "")
				return
			}
		}

		if err := ops.Resolve(resolveCtx); err != nil {
			_printStackTrace(w, err, "")
			return
		}

		if len(ops) > 1 {
			requests = append(requests, SqlRequest{sql: "SELECT '['::text as array_start", outputs: true})
		}

		for i, op := range ops {
			bf := bytebufferpool.Get()
			defer bytebufferpool.Put(bf)

			savepoint_name := ""

			if op.Rollback {
				savepoints++
				savepoint_name = "savepoint_" + strconv.Itoa(savepoints)
				requests = append(requests, SqlRequest{sql: "SAVEPOINT " + savepoint_name})
			}

			switch op.Verb {
			case query.OP_MERGE:
				// err = op.SqlUpsert(ctx, true, true)
				templates.GenerateFlatQuery(bf, op.Selection, true, true)

			case query.OP_INSERT:
				// err = op.SqlUpsert(ctx, false, false)
				templates.GenerateFlatQuery(bf, op.Selection, false, false)
			case query.OP_UPSERT:
				// err = op.SqlUpsert(ctx, false, true)
				templates.GenerateFlatQuery(bf, op.Selection, false, true)
			case query.OP_DELETE:
				templates.GenerateDeleteQuery(bf, op.Selection)
			}

			// os.Stdout.WriteString(bf.String())
			verb_str := bf.String()
			if verb_str != "" {
				if hasBody(r) {
					flats, err := op.FlattenJsonData(body)
					if err != nil {
						_printStackTrace(w, err, "")
						return
					}

					temp_str := "CREATE TEMP TABLE __flat_json_temp (___node_id bigint, ___parent_id bigint, ___selection_index int, json jsonb) ON COMMIT DROP"

					requests = append(requests, SqlRequest{sql: temp_str})
					requests = append(requests, SqlRequest{sql: "__flat_json_temp", args: flats})

				}
				requests = append(requests, SqlRequest{sql: verb_str})
			}
			// ctx.Buffer.Reset()

			ctx := query.NewContext(srv.Tables, srv.Functions, "select")
			ctx.Buffer = bytebufferpool.Get()
			defer bytebufferpool.Put(ctx.Buffer)
			ctx.HasBody = len(body) > 0

			if err := op.SqlSelect(ctx); err != nil {
				_printStackTrace(w, err, "")
				return
			}

			sel := ctx.Buffer.String()
			if sel != "" {
				requests = append(requests, SqlRequest{sql: sel, outputs: true})
			}

			if op.Rollback {
				requests = append(requests, SqlRequest{sql: "ROLLBACK TO SAVEPOINT " + savepoint_name})
			}

			if len(ops) > 1 {
				if i < len(ops)-1 {
					requests = append(requests, SqlRequest{sql: "select ','::text as res", outputs: true})
				} else {
					requests = append(requests, SqlRequest{sql: "select ']'::text as res", outputs: true})
				}
			}
		}

		runJsonArraySql(srv, w, r, requests)

	}
	router.Handle("/rel/*", http.HandlerFunc(relqlHandler))
	router.Handle("/rel", http.HandlerFunc(relqlHandler))

	router.Get("/_schema", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/json;charset=utf-8")
		json.NewEncoder(w).Encode(struct {
			Tables    pg.DBAllTables
			Functions map[string]*pg.Function
		}{
			Tables:    srv.Tables,
			Functions: srv.Functions,
		})
	})

}
