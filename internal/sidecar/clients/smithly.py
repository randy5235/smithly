"""Smithly sidecar client — zero dependencies (stdlib only)."""
import os, json, urllib.request

_API = os.environ.get("SMITHLY_API", "http://localhost:18791")
_TOKEN = os.environ.get("SMITHLY_TOKEN", "")
_H = {"Authorization": f"Bearer {_TOKEN}", "Content-Type": "application/json"}

def _post(path, body):
    r = urllib.request.urlopen(urllib.request.Request(
        f"{_API}{path}", json.dumps(body).encode(), _H))
    return json.loads(r.read())

def _get(path):
    r = urllib.request.urlopen(urllib.request.Request(f"{_API}{path}", headers=_H))
    return json.loads(r.read())

# --- Controller services (via sidecar) ---

def oauth2_token(provider):
    """Get a fresh bearer token for an OAuth2 provider."""
    return _get(f"/oauth2/{provider}")["token"]

def notify(title, message, priority=3):
    """Send a push notification."""
    return _post("/notify", {"title": title, "message": message, "priority": priority})

def audit(action, target="", details=""):
    """Log an audit entry."""
    return _post("/audit", {"action": action, "target": target, "details": details})

def secret(name):
    """Read a secret by name. Value never touches env vars."""
    return _get(f"/secrets/{name}")["value"]

# --- Abstracted store (optional — use these or connect to DB directly) ---

def store_put(type, data, public=False, id=None):
    """Create a new version of an object in the store."""
    obj = {"type": type, "data": data, "public": public}
    if id: obj["id"] = id
    return _post("/store/put", obj)

def store_get(id):
    """Get the latest version of an object by ID."""
    return _post("/store/get", {"id": id})

def store_delete(id):
    """Soft-delete an object."""
    return _post("/store/delete", {"id": id})

def store_query(type=None, filter=None, limit=100):
    """Query objects by type and optional filters."""
    q = {"limit": limit}
    if type: q["type"] = type
    if filter: q["filter"] = filter
    return _post("/store/query", q)

def store_history(id):
    """Get full version history of an object."""
    return _post("/store/history", {"id": id})
