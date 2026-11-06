package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

type server struct {
	cfg   *config
	llama *llamaServer
}

// apiError carries an HTTP status and a LibreTranslate-style error message.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// cors mimics LibreTranslate's CORS middleware.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Expose-Headers", "Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type translateRequest struct {
	texts        []string
	single       bool
	source       string
	target       string
	format       string
	alternatives int
	apiKey       string
}

var errInvalidJSON = &apiError{http.StatusBadRequest, "Invalid JSON format"}

// parseTranslateRequest accepts both JSON bodies (q as string or array of
// strings) and form-encoded bodies, like LibreTranslate.
func parseTranslateRequest(r *http.Request) (*translateRequest, error) {
	req := &translateRequest{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var raw struct {
			Q            json.RawMessage `json:"q"`
			Source       string          `json:"source"`
			Target       string          `json:"target"`
			Format       string          `json:"format"`
			Alternatives *int            `json:"alternatives"`
			APIKey       string          `json:"api_key"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			return nil, &apiError{http.StatusBadRequest, "Invalid JSON format"}
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, errInvalidJSON
		}
		var v any
		if len(raw.Q) > 0 && string(raw.Q) != "null" {
			if err := json.Unmarshal(raw.Q, &v); err != nil {
				return nil, errInvalidJSON
			}
			switch t := v.(type) {
			case string:
				req.texts = []string{t}
				req.single = true
			case []any:
				for _, item := range t {
					s, ok := item.(string)
					if !ok {
						return nil, errInvalidJSON
					}
					req.texts = append(req.texts, s)
				}
			default:
				return nil, errInvalidJSON
			}
		}
		req.source = raw.Source
		req.target = raw.Target
		req.format = raw.Format
		if raw.Alternatives != nil {
			req.alternatives = *raw.Alternatives
		}
		req.apiKey = raw.APIKey
	} else {
		if err := r.ParseForm(); err != nil {
			return nil, &apiError{http.StatusBadRequest, "Invalid request"}
		}
		req.texts = []string{r.PostFormValue("q")}
		req.single = true
		req.source = r.PostFormValue("source")
		req.target = r.PostFormValue("target")
		req.format = r.PostFormValue("format")
		req.apiKey = r.PostFormValue("api_key")
		if a := r.PostFormValue("alternatives"); a != "" {
			n, err := strconv.Atoi(a)
			if err != nil {
				return nil, &apiError{http.StatusBadRequest, "Invalid request: alternatives parameter is not a number"}
			}
			req.alternatives = n
		}
	}
	return req, nil
}

// checkAPIKey enforces the optional --api-key flag.
func (s *server) checkAPIKey(req *translateRequest) *apiError {
	if s.cfg.apiKey == "" {
		return nil
	}
	if req.apiKey == "" {
		return &apiError{http.StatusBadRequest, "Please contact the server operator to get an API key"}
	}
	if req.apiKey != s.cfg.apiKey {
		return &apiError{http.StatusForbidden, "Invalid API key"}
	}
	return nil
}

// maxTokensFor picks a generation budget that scales with the input length
// so long inputs are not cut off at the flag default.
func (s *server) maxTokensFor(text string) int {
	est := int(float64(utf8.RuneCountInString(text))*1.2) + 64
	m := s.cfg.maxTokens
	if est > m {
		m = est
	}
	if lim := s.cfg.nCtx - 256; m > lim {
		m = lim
	}
	if m < 32 {
		m = 32
	}
	return m
}

func (s *server) handleTranslate(w http.ResponseWriter, r *http.Request) {
	req, err := parseTranslateRequest(r)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			writeError(w, ae.status, ae.msg)
		} else {
			writeError(w, http.StatusBadRequest, "Invalid request")
		}
		return
	}
	if ae := s.checkAPIKey(req); ae != nil {
		writeError(w, ae.status, ae.msg)
		return
	}

	// Required-parameter validation, in LibreTranslate's order and wording.
	if len(req.texts) == 0 || (req.single && req.texts[0] == "") {
		writeError(w, http.StatusBadRequest, "Invalid request: missing q parameter")
		return
	}
	if req.source == "" {
		writeError(w, http.StatusBadRequest, "Invalid request: missing source parameter")
		return
	}
	if req.target == "" {
		writeError(w, http.StatusBadRequest, "Invalid request: missing target parameter")
		return
	}
	if req.alternatives < 0 {
		req.alternatives = 0
	}
	format := req.format
	if format == "" {
		format = "text"
	}
	if format != "text" && format != "html" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s format is not supported", format))
		return
	}

	srcPlamo := ""
	if req.source != "auto" {
		srcPlamo = toPlamo(req.source)
		if srcPlamo == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s is not supported", req.source))
			return
		}
	}
	tgtPlamo := toPlamo(req.target)
	if tgtPlamo == "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s is not supported", req.target))
		return
	}

	if s.cfg.charLimit > 0 {
		for _, t := range req.texts {
			if n := utf8.RuneCountInString(t); n > s.cfg.charLimit {
				writeError(w, http.StatusBadRequest, fmt.Sprintf(
					"Invalid request: request (%d) exceeds text limit (%d)", n, s.cfg.charLimit))
				return
			}
		}
	}

	// With source=auto, report one detected language shared by the batch,
	// like LibreTranslate; the model itself also auto-detects when no
	// source_lang is passed, so detection quality stays on the model side.
	var detected *detection
	if req.source == "auto" {
		ds := detectLanguage(joinTexts(req.texts))
		if len(ds) > 0 {
			detected = &ds[0]
		} else {
			detected = &detection{Confidence: 0, Language: "en"}
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.requestTimeout)
	defer cancel()

	translateOne := func(text string) (string, bool, error) {
		// Non-translatable input (numbers, punctuation only) is returned
		// unchanged, matching LibreTranslate's behavior.
		if format == "html" {
			if !htmlHasTranslatableText(text) {
				return text, false, nil
			}
			out, err := s.translateHTML(ctx, text, srcPlamo, tgtPlamo)
			return out, true, err
		}
		if !translatable(text) {
			return text, false, nil
		}
		out, err := s.llama.complete(ctx, text, srcPlamo, tgtPlamo, 0.0, s.maxTokensFor(text))
		return out, true, err
	}

	results := make([]string, len(req.texts))
	altLists := make([][]string, len(req.texts))
	for i, text := range req.texts {
		out, translated, err := translateOne(text)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Cannot translate text: "+err.Error())
			return
		}
		results[i] = out
		if !translated {
			continue
		}
		if req.alternatives > 0 {
			seen := map[string]bool{out: true}
			alts := []string{}
			for j := 0; j < req.alternatives; j++ {
				alt, err := s.llama.complete(ctx, text, srcPlamo, tgtPlamo, 0.7, s.maxTokensFor(text))
				if err != nil {
					break
				}
				if !seen[alt] {
					seen[alt] = true
					alts = append(alts, alt)
				}
			}
			altLists[i] = alts
		}
	}

	resp := map[string]any{}
	if req.single {
		resp["translatedText"] = results[0]
		if detected != nil {
			resp["detectedLanguage"] = *detected
		}
		if req.alternatives > 0 {
			resp["alternatives"] = altLists[0]
		}
	} else {
		resp["translatedText"] = results
		if detected != nil {
			arr := make([]detection, len(req.texts))
			for i := range arr {
				arr[i] = *detected
			}
			resp["detectedLanguage"] = arr
		}
		if req.alternatives > 0 {
			resp["alternatives"] = altLists
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleLanguages(w http.ResponseWriter, r *http.Request) {
	targets := allISOCodes
	out := make([]map[string]any, 0, len(languages))
	for _, l := range languages {
		out = append(out, map[string]any{
			"code":    l.iso,
			"name":    l.name,
			"targets": targets,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleDetect(w http.ResponseWriter, r *http.Request) {
	var q string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var raw struct {
			Q      string `json:"q"`
			APIKey string `json:"api_key"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil || json.Unmarshal(body, &raw) != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON format")
			return
		}
		q = raw.Q
		if s.cfg.apiKey != "" && raw.APIKey == "" {
			writeError(w, http.StatusBadRequest, "Please contact the server operator to get an API key")
			return
		}
		if s.cfg.apiKey != "" && raw.APIKey != s.cfg.apiKey {
			writeError(w, http.StatusForbidden, "Invalid API key")
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request")
			return
		}
		q = r.PostFormValue("q")
		key := r.PostFormValue("api_key")
		if s.cfg.apiKey != "" && key == "" {
			writeError(w, http.StatusBadRequest, "Please contact the server operator to get an API key")
			return
		}
		if s.cfg.apiKey != "" && key != s.cfg.apiKey {
			writeError(w, http.StatusForbidden, "Invalid API key")
			return
		}
	}
	if q == "" {
		writeError(w, http.StatusBadRequest, "Invalid request: missing q parameter")
		return
	}
	writeJSON(w, http.StatusOK, detectLanguage(q))
}

func (s *server) handleFrontendSettings(w http.ResponseWriter, r *http.Request) {
	hasKey := s.cfg.apiKey != ""
	writeJSON(w, http.StatusOK, map[string]any{
		"charLimit":            s.cfg.charLimit,
		"frontendTimeout":      500,
		"apiKeys":              hasKey,
		"keyRequired":          hasKey,
		"suggestions":          false,
		"filesTranslation":     false,
		"supportedFilesFormat": []string{},
		"language": map[string]any{
			"source": map[string]string{"code": "en", "name": "English"},
			"target": map[string]string{"code": "ja", "name": "Japanese"},
		},
	})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
