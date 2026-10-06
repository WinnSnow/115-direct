package httpapi

import (
	"net/http"

	"github.com/local/115-direct/internal/pan115"
)

func (s *Server) account115(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.Pan.(pan115.AccountProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "account_unavailable", "当前 115 驱动不支持账号信息")
		return
	}
	account, err := provider.Account(r.Context(), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeError(w, http.StatusBadGateway, "account_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, account)
}
