package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func Middleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check for RapidAPI proxy secret first in production
			if env := r.Header.Get("X-Environment"); env == "production" {
				proxySecret := r.Header.Get("X-RapidAPI-Proxy-Secret")
				expectedSecret := r.Header.Get("X-Expected-Proxy-Secret")
				
				if proxySecret == "" || expectedSecret == "" {
					http.Error(w, `{"error":"rapidapi proxy secret required"}`, http.StatusUnauthorized)
					return
				}
				
				// Use constant-time comparison for proxy secret
				if subtle.ConstantTimeCompare([]byte(proxySecret), []byte(expectedSecret)) != 1 {
					http.Error(w, `{"error":"invalid rapidapi proxy secret"}`, http.StatusUnauthorized)
					return
				}
			}
			
			// Check API key for development or additional validation
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"authorization header required"}`, http.StatusUnauthorized)
				return
			}

			// Remove "Bearer " prefix
			token := strings.TrimPrefix(authHeader, "Bearer ")
			
			// Use constant-time comparison for API key
			if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
				http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
