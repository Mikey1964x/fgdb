# Test data

- `Querying.gdb`, `Domains.gdb`, `Shapes.gdb`: sample geodatabases from the
  [Esri FileGDB API 1.5](https://github.com/Esri/file-geodatabase-api)
  (`samples/data`), distributed under the Apache License 2.0.
  `Shapes.gdb` omits the 42 MB multipatch table (`a00000027`).
- `gdal.gdb`: written by GDAL's OpenFileGDB driver with
  `tools/make_gdal_gdb.py`. It covers every field type and geometry type
  GDAL can write (Z/M values, NULLs, curves, multipatches, feature datasets).
- `expected/`: reference values read with GDAL by `tools/make_expected.py`,
  used by `gdal_compare_test.go`.

To regenerate (needs Python with `pyogrio`, `numpy` and `shapely`):

    python3 -I testdata/tools/make_gdal_gdb.py testdata/gdal.gdb
    python3 -I testdata/tools/make_expected.py testdata/expected testdata/*.gdb
