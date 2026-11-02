package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
)

// Design storage API (all routes require a session):
//
//	GET    /api/designs                        saved designs, most recent first
//	POST   /api/designs                        create {name, note?, architecture} → meta
//	GET    /api/designs/{id}                   meta + version list
//	POST   /api/designs/{id}/versions          save a new version → meta (200 if unchanged)
//	GET    /api/designs/{id}/versions/{n}      one version with its architecture
//	DELETE /api/designs/{id}                   delete a design and all its versions

// maxDesignBytes matches the editor's share-link cap: a design is a few KB.
const maxDesignBytes = 2 << 20

type saveRequest struct {
	Name         string          `json:"name"`
	Note         string          `json:"note"`
	Architecture json.RawMessage `json:"architecture"`
}

func designRoutes(mux *http.ServeMux, s *store) {
	mux.HandleFunc("GET /api/designs", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.list()
		respond(w, http.StatusOK, list, err)
	})
	mux.HandleFunc("POST /api/designs", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeSave(w, r)
		if !ok {
			return
		}
		m, err := s.create(req.Name, req.Note, req.Architecture)
		respond(w, http.StatusCreated, m, err)
	})
	mux.HandleFunc("GET /api/designs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		m, err := s.meta(id)
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		vs, err := s.versions(id)
		respond(w, http.StatusOK, struct {
			designMeta
			History []versionInfo `json:"history"`
		}{m, vs}, err)
	})
	mux.HandleFunc("POST /api/designs/{id}/versions", func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeSave(w, r)
		if !ok {
			return
		}
		m, err := s.save(r.PathValue("id"), req.Name, req.Note, req.Architecture)
		status := http.StatusCreated
		if errors.Is(err, errUnchanged) {
			status, err = http.StatusOK, nil
		}
		respond(w, status, m, err)
	})
	mux.HandleFunc("GET /api/designs/{id}/versions/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil {
			respondError(w, http.StatusNotFound, "not found")
			return
		}
		v, err := s.version(r.PathValue("id"), n)
		respond(w, http.StatusOK, v, err)
	})
	mux.HandleFunc("DELETE /api/designs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			respondError(w, http.StatusForbidden, "cross-origin request")
			return
		}
		if err := s.delete(r.PathValue("id")); err != nil {
			respond(w, 0, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// decodeSave reads a save request. Requiring a JSON content type means a
// cross-site form cannot send it, and the Origin check covers the rest.
func decodeSave(w http.ResponseWriter, r *http.Request) (saveRequest, bool) {
	var req saveRequest
	if !sameOrigin(r) {
		respondError(w, http.StatusForbidden, "cross-origin request")
		return req, false
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		respondError(w, http.StatusUnsupportedMediaType, "expected application/json")
		return req, false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDesignBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return req, false
	}
	if err := checkDesign(req.Name, req.Note, req.Architecture); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return req, false
	}
	return req, true
}

// respond writes v with status, or the error's status when err is set.
func respond(w http.ResponseWriter, status int, v any, err error) {
	switch {
	case errors.Is(err, errNotFound):
		respondError(w, http.StatusNotFound, "not found")
	case err != nil:
		slog.Error("design storage", "err", err)
		respondError(w, http.StatusInternalServerError, "storage error")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
}

func respondError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
