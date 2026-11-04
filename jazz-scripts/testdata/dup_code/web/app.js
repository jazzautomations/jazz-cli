// app entry
export function parseQuery(search) {
  const out = {};
  for (const part of search.replace(/^\?/, "").split("&")) {
    if (!part) continue;
    const [k, v] = part.split("=");
    out[decodeURIComponent(k)] = decodeURIComponent(v || "");
  }
  return out;
}
console.log(parseQuery(location.search));
