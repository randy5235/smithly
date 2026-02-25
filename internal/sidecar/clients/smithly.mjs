// Smithly sidecar client — zero dependencies (uses built-in fetch).
const API = process.env.SMITHLY_API || "http://localhost:18791";
const TOKEN = process.env.SMITHLY_TOKEN || "";
const h = {"Authorization": `Bearer ${TOKEN}`, "Content-Type": "application/json"};

const post = async (p, b) => (await fetch(`${API}${p}`, {method:"POST", headers:h, body:JSON.stringify(b)})).json();
const get = async (p) => (await fetch(`${API}${p}`, {headers:h})).json();

// Controller services
export const oauth2Token = (p) => get(`/oauth2/${p}`).then(r => r.token);
export const notify = (title, message, priority=3) => post("/notify", {title, message, priority});
export const audit = (action, target="", details="") => post("/audit", {action, target, details});
export const secret = (name) => get(`/secrets/${name}`).then(r => r.value);

// Abstracted store (optional)
export const storePut = (type, data, pub=false, id) => post("/store/put", {type, data, public: pub, ...(id && {id})});
export const storeGet = (id) => post("/store/get", {id});
export const storeDelete = (id) => post("/store/delete", {id});
export const storeQuery = (opts) => post("/store/query", opts);
export const storeHistory = (id) => post("/store/history", {id});
