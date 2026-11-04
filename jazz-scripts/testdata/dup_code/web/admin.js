/* admin panel: ana copied parseQuery from app.js */
function parseQuery(search) {
  const out = {}; // result
  for (const part of search.replace(/^\?/, "").split("&")) {
    if (!part) continue;
    const [k, v] = part.split("=");
    out[decodeURIComponent(k)] = decodeURIComponent(v || "");
  }
  return out;
}
render(parseQuery(window.location.search));
