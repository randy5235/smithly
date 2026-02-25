#!/usr/bin/env bash
# Smithly sidecar client — requires curl and jq.

_get()  { curl -sf -H "Authorization: Bearer $SMITHLY_TOKEN" "$SMITHLY_API$1"; }
_post() { curl -sf -H "Authorization: Bearer $SMITHLY_TOKEN" \
  -H "Content-Type: application/json" -d "$2" "$SMITHLY_API$1"; }

# Controller services
smithly_token()  { _get "/oauth2/$1" | jq -r .token; }
smithly_notify() { _post "/notify" "{\"title\":\"$1\",\"message\":\"$2\",\"priority\":${3:-3}}"; }
smithly_audit()  { _post "/audit" "{\"action\":\"$1\",\"target\":\"$2\"}"; }
smithly_secret() { _get "/secrets/$1" | jq -r .value; }

# Abstracted store (optional)
smithly_store_put()   { _post "/store/put" "$1"; }
smithly_store_get()   { _post "/store/get" "{\"id\":\"$1\"}"; }
smithly_store_delete(){ _post "/store/delete" "{\"id\":\"$1\"}"; }
smithly_store_query() { _post "/store/query" "$1"; }
smithly_store_history(){ _post "/store/history" "{\"id\":\"$1\"}"; }
