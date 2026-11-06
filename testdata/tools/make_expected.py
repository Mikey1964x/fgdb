"""Extracts reference values from geodatabases with GDAL (via pyogrio).

For each layer it writes testdata/expected/<gdb>/<layer>.json.gz holding the
ObjectIDs, attribute values and geometry coordinates (grouped by part) as
read by GDAL's OpenFileGDB driver. The Go tests compare the package output
with these files. Run with:

    python3 -I testdata/tools/make_expected.py testdata/expected testdata/*.gdb
"""
import gzip, json, math, os, sys, warnings
import numpy as np
import pyogrio
from pyogrio import raw
import shapely

warnings.filterwarnings("ignore")
outdir = sys.argv[1]


def jsonable(v):
    if v is None:
        return None
    if isinstance(v, (np.integer,)):
        return int(v)
    if isinstance(v, (np.floating, float)):
        f = float(v)
        return None if math.isnan(f) else f
    if isinstance(v, bytes):
        return {"hex": v.hex()}
    return str(v)


def parts_of(g):
    """Coordinates grouped by part, in shape buffer part order."""
    t = g.geom_type
    if t == "Point":
        return [coords([g])]
    if t == "MultiPoint":
        return [coords(list(g.geoms))]
    if t == "LineString":
        return [coords_of(g)]
    if t == "MultiLineString":
        return [coords_of(l) for l in g.geoms]
    if t == "Polygon":
        return [coords_of(g.exterior)] + [coords_of(r) for r in g.interiors]
    if t == "MultiPolygon":
        out = []
        for p in g.geoms:
            out += parts_of(p)
        return out
    return None


def coords(points):
    return [c for p in points for c in coords_of(p)]


def coords_of(g):
    hz, hm = g.has_z, getattr(g, "has_m", False)
    a = shapely.get_coordinates(g, include_z=hz, include_m=hm)
    return [[None if math.isnan(x) else float(x) for x in row] for row in a.tolist()]


for gdb in sys.argv[2:]:
    name = os.path.basename(gdb.rstrip("/"))
    os.makedirs(os.path.join(outdir, name), exist_ok=True)
    for idx in range(1000):
        try:
            meta, fids, geoms, fields = raw.read(gdb, layer=idx, return_fids=True, datetime_as_string=True)
        except Exception as e:  # end of layers, or unsupported by GDAL
            if "could not be opened" in str(e) or "out of range" in str(e):
                break
            print("skip", name, idx, e)
            continue
        layer = meta["layer_name"] if "layer_name" in meta else None
        if layer is None:
            layer = pyogrio.read_info(gdb, layer=idx)["layer_name"]
        rows = []
        for i, fid in enumerate(fids):
            r = {"fid": int(fid), "attrs": {n: jsonable(fields[j][i]) for j, n in enumerate(meta["fields"])}}
            if geoms is not None:
                if geoms[i] is None:
                    r["geom"] = None
                else:
                    try:
                        g = shapely.from_wkb(geoms[i])
                        p = parts_of(g)
                        r["geom"] = {"type": g.geom_type, "parts": p} if p is not None else {"type": g.geom_type}
                    except Exception:
                        r["geom"] = {"type": "unparsed"}
            rows.append(r)
        doc = {"layer": layer, "geometry_type": meta.get("geometry_type"), "fields": list(meta["fields"]), "rows": rows}
        with gzip.open(os.path.join(outdir, name, layer + ".json.gz"), "wt") as f:
            json.dump(doc, f, indent=None, separators=(",", ":"))
        print("wrote", name, layer, len(rows))
