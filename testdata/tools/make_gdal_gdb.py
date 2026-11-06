"""Builds testdata/gdal.gdb with GDAL's OpenFileGDB driver (via pyogrio).

The geodatabase covers every field type and geometry type that GDAL can
write, including NULLs, Z/M values, curves and multipatches. Run with:

    python3 -I testdata/tools/make_gdal_gdb.py testdata/gdal.gdb
"""
import os, shutil, struct, sys
import numpy as np
from pyogrio import raw

out = sys.argv[1]
if os.path.exists(out):
    shutil.rmtree(out)

def pt(c):
    return struct.pack("<%dd" % len(c), *c)

def dims(z, m):
    return 3000 if z and m else 1000 if z else 2000 if m else 0

def wkb_point(c, z=False, m=False):
    return struct.pack("<BI", 1, 1 + dims(z, m)) + pt(c)

def wkb_ls(coords, z=False, m=False, t=2):
    b = struct.pack("<BII", 1, t + dims(z, m), len(coords))
    return b + b"".join(pt(c) for c in coords)

def wkb_poly(rings, z=False, m=False, t=3):
    b = struct.pack("<BII", 1, t + dims(z, m), len(rings))
    for r in rings:
        b += struct.pack("<I", len(r)) + b"".join(pt(c) for c in r)
    return b

def wkb_multi(t, parts, z=False, m=False):
    return struct.pack("<BII", 1, t + dims(z, m), len(parts)) + b"".join(parts)

WGS84 = 'GEOGCS["WGS 84",DATUM["WGS_1984",SPHEROID["WGS 84",6378137,298.257223563]],PRIMEM["Greenwich",0],UNIT["degree",0.0174532925199433],AUTHORITY["EPSG","4326"]]'
UTM = 'PROJCS["WGS 84 / UTM zone 31N",GEOGCS["WGS 84",DATUM["WGS_1984",SPHEROID["WGS 84",6378137,298.257223563]],PRIMEM["Greenwich",0],UNIT["degree",0.0174532925199433]],PROJECTION["Transverse_Mercator"],PARAMETER["latitude_of_origin",0],PARAMETER["central_meridian",3],PARAMETER["scale_factor",0.9996],PARAMETER["false_easting",500000],PARAMETER["false_northing",0],UNIT["metre",1],AUTHORITY["EPSG","32631"]]'
PRO32 = ["TARGET_ARCGIS_VERSION=ARCGIS_PRO_3_2_OR_LATER"]

def write(layer, geoms, fields, gtype=None, crs=None, opts=None, masks=None, append=False):
    names = list(fields.keys())
    data = [fields[n] for n in names]
    raw.write(out, np.array(geoms, dtype=object) if geoms is not None else None, data, names,
              field_mask=masks, layer=layer, driver="OpenFileGDB", geometry_type=gtype, crs=crs,
              layer_options=opts or {}, append=append)

# 1. Attribute table with every field type and NULLs.
n = 4
ints16 = np.array([1, -2, 300, 0], dtype=np.int16)
write("alltypes", None, {
    "f_int16": ints16,
    "f_int32": np.array([1, -100000, 2147483647, 0], dtype=np.int32),
    "f_int64": np.array([1, -5000000000, 9007199254740993, 0], dtype=np.int64),
    "f_float32": np.array([1.5, -2.25, 3.0e10, 0], dtype=np.float32),
    "f_float64": np.array([1.0 / 3, -1e300, 2.5, 0], dtype=np.float64),
    "f_string": np.array(["hello", "héllo wörld ✓", "", None], dtype=object),
    "f_datetime": np.array(["2020-01-02T03:04:05.678", "1899-12-30T00:00:00", "2100-12-31T23:59:59", "NaT"], dtype="datetime64[ms]"),
    "f_date": np.array(["2020-01-02", "1970-01-01", "1800-06-15", "NaT"], dtype="datetime64[D]"),
}, masks=[np.array([False, False, False, True])] * 5 + [None, None, None], opts={"TARGET_ARCGIS_VERSION": "ARCGIS_PRO_3_2_OR_LATER"})

# 2. Points with Z and M.
write("points_zm", [
    wkb_point((1.5, 2.5, 10.0, 100.0), True, True),
    wkb_point((-120.123456789, 45.987654321, -5.5, 0.0), True, True),
    None,
    wkb_point((179.9, -89.9, 8848.0, -1.0), True, True),
], {"name": np.array(["a", "b", "nullgeom", "d"], dtype=object)}, gtype="Measured 3D Point", crs=WGS84)

# 3. Multipoints with Z.
write("multipoints_z", [
    wkb_multi(4, [wkb_point((500000 + i * 10, 4000000 + i * 3, i * 1.25), True) for i in range(5)], True),
    wkb_multi(4, [wkb_point((600000, 4100000, 1.0), True)], True),
], {"id": np.array([10, 20], dtype=np.int32)}, gtype="MultiPoint Z", crs=UTM)

# 4. Lines with M.
write("lines_m", [
    wkb_ls([(0, 0, 0), (10, 0, 5), (10, 10, 12.5)], m=True),
    wkb_multi(5, [wkb_ls([(20, 20, 1), (30, 30, 2)], m=True), wkb_ls([(40, 40, 3), (50, 45, 4), (60, 40, 5)], m=True)], m=True),
], {"id": np.array([1, 2], dtype=np.int32)}, gtype="Measured MultiLineString", crs=UTM)

# 5. Polygons with holes and multiple parts.
sq = lambda x0, y0, s: [(x0, y0), (x0, y0 + s), (x0 + s, y0 + s), (x0 + s, y0), (x0, y0)]
write("polygons", [
    wkb_poly([sq(0, 0, 10)]),
    wkb_poly([sq(0, 0, 100), list(reversed(sq(10, 10, 20))), list(reversed(sq(50, 50, 20)))]),
    wkb_multi(6, [wkb_poly([sq(200, 200, 10)]), wkb_poly([sq(300, 300, 50), list(reversed(sq(310, 310, 5)))])]),
], {"label": np.array(["square", "holes", "multi"], dtype=object)}, gtype="MultiPolygon", crs=UTM)

# 6. Curves (stored as general polylines / polygons).
circ = struct.pack("<BII", 1, 8, 3) + pt((0, 0)) + pt((1, 1)) + pt((2, 0))
comp = struct.pack("<BII", 1, 9, 2) + circ + wkb_ls([(2, 0), (3, 0)])
write("curves_line", [
    struct.pack("<BII", 1, 11, 1) + comp,
    wkb_multi(5, [wkb_ls([(0, 0), (5, 5)])]),
], {"id": np.array([1, 2], dtype=np.int32)}, gtype="MultiLineString", crs=UTM)
ring = struct.pack("<BII", 1, 8, 5) + pt((0, 0)) + pt((0, 10)) + pt((10, 10)) + pt((10, 0)) + pt((0, 0))
write("curves_polygon", [
    struct.pack("<BII", 1, 12, 1) + struct.pack("<BII", 1, 10, 1) + ring,
], {"id": np.array([1], dtype=np.int32)}, gtype="MultiPolygon", crs=UTM)

# 7. Multipatch, from a MultiPolygon Z.
mpz = wkb_multi(6, [
    wkb_poly([[(0, 0, 0), (0, 1, 1), (1, 0, 2), (0, 0, 0)]], z=True),
    wkb_poly([[(10, 10, 5), (10, 20, 5), (20, 20, 6), (20, 10, 6), (10, 10, 5)],
              [(12, 12, 5), (14, 12, 5), (14, 14, 5), (12, 14, 5), (12, 12, 5)]], z=True),
], z=True)
tri = lambda a, b, c: struct.pack("<BII", 1, 1017, 1) + struct.pack("<I", 4) + pt(a) + pt(b) + pt(c) + pt(a)
tin = struct.pack("<BII", 1, 1016, 2) + tri((0, 0, 0), (0, 1, 1), (1, 0, 2)) + tri((1, 0, 2), (0, 1, 1), (1, 1, 3))
phs = struct.pack("<BII", 1, 1015, 2) + mpz[9:]
write("multipatch", [tin, phs], {"id": np.array([1, 2], dtype=np.int32)}, gtype="MultiPolygon Z", crs=UTM,
      opts={"CREATE_MULTIPATCH": "YES"})

# 8. A feature class inside a feature dataset.
write("in_dataset", [wkb_point((500100, 4000100))], {"id": np.array([7], dtype=np.int32)},
      gtype="Point", crs=UTM, opts={"FEATURE_DATASET": "fds"})

print("wrote", out)
