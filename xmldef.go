package fgdb

import (
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Mikey1964x/fgdb/internal/gdbtable"
)

// Item definitions stored in GDB_Items.Definition follow the layout GDAL's
// OpenFileGDB driver writes, which ArcGIS reads.

const (
	xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	xmlNS     = `xmlns:typens="http://www.esri.com/schemas/ArcGIS/10.3" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xs="http://www.w3.org/2001/XMLSchema"`
)

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

type xmlWriter struct{ strings.Builder }

func (w *xmlWriter) elem(name, value string) {
	fmt.Fprintf(w, "<%s>%s</%s>", name, xmlEscape(value), name)
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'g', 17, 64) }

func (w *xmlWriter) spatialReference(gi *gdbtable.GeomInfo, wkid int) {
	w.WriteString(`<Extent xsi:nil="true"></Extent>`)
	switch {
	case gi.WKT == "" || gi.WKT == unknownSRS:
		w.WriteString(`<SpatialReference xsi:type="typens:UnknownCoordinateSystem">`)
	case strings.HasPrefix(strings.ToUpper(gi.WKT), "GEOGCS"):
		w.WriteString(`<SpatialReference xsi:type="typens:GeographicCoordinateSystem">`)
		w.elem("WKT", gi.WKT)
	default:
		w.WriteString(`<SpatialReference xsi:type="typens:ProjectedCoordinateSystem">`)
		w.elem("WKT", gi.WKT)
	}
	for _, kv := range []struct {
		k string
		v float64
	}{
		{"XOrigin", gi.XOrigin}, {"YOrigin", gi.YOrigin}, {"XYScale", gi.XYScale},
		{"ZOrigin", gi.ZOrigin}, {"ZScale", gi.ZScale}, {"MOrigin", gi.MOrigin}, {"MScale", gi.MScale},
		{"XYTolerance", gi.XYTolerance}, {"ZTolerance", gi.ZTolerance}, {"MTolerance", gi.MTolerance},
	} {
		w.elem(kv.k, fmtFloat(kv.v))
	}
	w.elem("HighPrecision", "true")
	if wkid > 0 {
		w.elem("WKID", strconv.Itoa(wkid))
		w.elem("LatestWKID", strconv.Itoa(wkid))
	}
	w.WriteString(`</SpatialReference>`)
}

func featureDatasetXML(path, name string, dsid int, gi *gdbtable.GeomInfo, wkid int) string {
	var w xmlWriter
	w.WriteString(xmlHeader)
	w.WriteString(`<typens:DEFeatureDataset ` + xmlNS + ` xsi:type="typens:DEFeatureDataset">`)
	w.elem("CatalogPath", path)
	w.elem("Name", name)
	w.elem("ChildrenExpanded", "false")
	w.elem("DatasetType", "esriDTFeatureDataset")
	w.elem("DSID", strconv.Itoa(dsid))
	w.elem("Versioned", "false")
	w.elem("CanVersion", "false")
	w.spatialReference(gi, wkid)
	w.WriteString(`</typens:DEFeatureDataset>`)
	return w.String()
}

var shapeTypeNames = map[uint8]string{
	1: "esriGeometryPoint", 2: "esriGeometryMultipoint", 3: "esriGeometryPolyline",
	4: "esriGeometryPolygon", 9: "esriGeometryMultiPatch",
}

func fieldXML(w *xmlWriter, d FieldDef, f *gdbtable.Field) {
	w.WriteString(`<GPFieldInfoEx xsi:type="typens:GPFieldInfoEx">`)
	w.elem("Name", d.Name)
	if f.Alias != "" {
		w.elem("AliasName", f.Alias)
	}
	length := 0
	switch d.Type {
	case FieldTypeOID:
		w.elem("FieldType", d.Type.String())
		w.elem("IsNullable", "false")
		w.elem("Length", "4")
		w.elem("Precision", "0")
		w.elem("Scale", "0")
		w.elem("Required", "true")
		w.WriteString(`</GPFieldInfoEx>`)
		return
	case FieldTypeGeometry:
		w.elem("FieldType", d.Type.String())
		w.elem("IsNullable", strconv.FormatBool(f.Nullable()))
		w.elem("Length", "0")
		w.elem("Precision", "0")
		w.elem("Scale", "0")
		w.elem("Required", "true")
		w.WriteString(`</GPFieldInfoEx>`)
		return
	case FieldTypeString:
		length = int(f.MaxWidth)
	default:
		length = int(f.Width)
		if d.Type == FieldTypeGUID || d.Type == FieldTypeGlobalID {
			length = 38
		}
	}
	switch v := d.DefaultValue.(type) {
	case nil:
	case string:
		w.elem("DefaultValueString", v)
	default:
		if d.Type == FieldTypeInteger || d.Type == FieldTypeSmallInteger {
			n, _ := toInt64(v)
			fmt.Fprintf(w, `<DefaultValue xsi:type="xs:%s">%d</DefaultValue>`, map[bool]string{true: "short", false: "int"}[d.Type == FieldTypeSmallInteger], n)
		} else if d.Type == FieldTypeBigInteger {
			n, _ := toInt64(v)
			w.elem("DefaultValueInteger", strconv.FormatInt(n, 10))
		} else if x, ok := toFloat64(v); ok {
			w.elem("DefaultValueNumeric", fmtFloat(x))
		} else if len(f.Default) == 8 || len(f.Default) == 10 {
			w.elem("DefaultValueNumeric", fmtFloat(math.Float64frombits(gdbtableLE64(f.Default))))
		}
	}
	w.elem("FieldType", d.Type.String())
	w.elem("IsNullable", strconv.FormatBool(f.Nullable()))
	if f.Required() {
		w.elem("Required", "true")
	}
	if !f.Editable() {
		w.elem("Editable", "false")
	}
	if d.Type == FieldTypeDate {
		w.elem("HighPrecision", "true")
	}
	w.elem("Length", strconv.Itoa(length))
	w.elem("Precision", "0")
	w.elem("Scale", "0")
	w.WriteString(`</GPFieldInfoEx>`)
}

func gdbtableLE64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

func tableXML(path, name string, dsid int, o gdbtable.CreateOptions, keyword string, defs []FieldDef) string {
	isFC := o.GeomType != 0
	kind := "DETableInfo"
	if isFC {
		kind = "DEFeatureClassInfo"
	}
	var w xmlWriter
	w.WriteString(xmlHeader)
	w.WriteString(`<typens:` + kind + ` ` + xmlNS + ` xsi:type="typens:` + kind + `">`)
	w.elem("CatalogPath", path)
	w.elem("Name", name)
	w.elem("ChildrenExpanded", "false")
	if isFC {
		w.elem("DatasetType", "esriDTFeatureClass")
	} else {
		w.elem("DatasetType", "esriDTTable")
	}
	w.elem("DSID", strconv.Itoa(dsid))
	w.elem("Versioned", "false")
	w.elem("CanVersion", "false")
	if keyword != "" {
		w.elem("ConfigurationKeyword", strings.ToUpper(keyword))
	}
	newTypes := false
	for _, d := range defs {
		newTypes = newTypes || d.Type >= FieldTypeBigInteger
	}
	if newTypes {
		w.elem("RequiredGeodatabaseClientVersion", "13.2")
	}
	w.elem("HasOID", "true")
	var geom *gdbtable.Field
	var geomName string
	for i, d := range defs {
		if d.Type == FieldTypeOID {
			w.elem("OIDFieldName", d.Name)
		}
		if o.Fields[i].Geom != nil {
			geom, geomName = o.Fields[i], d.Name
		}
	}
	w.WriteString(`<GPFieldInfoExs xsi:type="typens:ArrayOfGPFieldInfoEx">`)
	for i, d := range defs {
		fieldXML(&w, d, o.Fields[i])
	}
	w.WriteString(`</GPFieldInfoExs>`)
	if isFC {
		w.elem("CLSID", "{52353152-891A-11D0-BEC6-00805F7C4268}")
	} else {
		w.elem("CLSID", "{7A566981-C114-11D2-8A28-006097AFF44E}")
	}
	w.elem("EXTCLSID", "")
	w.elem("IsTimeInUTC", "true")
	if isFC {
		w.elem("FeatureType", "esriFTSimple")
		w.elem("ShapeType", shapeTypeNames[o.GeomType])
		w.elem("ShapeFieldName", geomName)
		w.elem("HasM", strconv.FormatBool(o.HasM))
		w.elem("HasZ", strconv.FormatBool(o.HasZ))
		w.elem("HasSpatialIndex", "true")
		w.elem("AreaFieldName", "")
		w.elem("LengthFieldName", "")
		wkid := 0
		for _, d := range defs {
			if d.GeometryDef != nil {
				wkid = d.GeometryDef.SpatialReference.WKID
			}
		}
		w.spatialReference(geom.Geom, wkid)
	}
	w.WriteString(`</typens:` + kind + `>`)
	return w.String()
}

// parseSpatialReferenceXML reads the SpatialReference element of an item
// definition.
func parseSpatialReferenceXML(def string) (SpatialReference, error) {
	var d struct {
		SR struct {
			WKT         string  `xml:"WKT"`
			XOrigin     float64 `xml:"XOrigin"`
			YOrigin     float64 `xml:"YOrigin"`
			XYScale     float64 `xml:"XYScale"`
			ZOrigin     float64 `xml:"ZOrigin"`
			ZScale      float64 `xml:"ZScale"`
			MOrigin     float64 `xml:"MOrigin"`
			MScale      float64 `xml:"MScale"`
			XYTolerance float64 `xml:"XYTolerance"`
			ZTolerance  float64 `xml:"ZTolerance"`
			MTolerance  float64 `xml:"MTolerance"`
			WKID        int     `xml:"WKID"`
			LatestWKID  int     `xml:"LatestWKID"`
		} `xml:"SpatialReference"`
	}
	if err := xml.Unmarshal([]byte(def), &d); err != nil {
		return SpatialReference{}, err
	}
	s := d.SR
	inv := func(x float64) float64 {
		if x == 0 {
			return 0
		}
		return 1 / x
	}
	sr := SpatialReference{
		WKT: s.WKT, WKID: s.LatestWKID,
		FalseX: s.XOrigin, FalseY: s.YOrigin, XYResolution: inv(s.XYScale), XYTolerance: s.XYTolerance,
		FalseZ: s.ZOrigin, ZResolution: inv(s.ZScale), ZTolerance: s.ZTolerance,
		FalseM: s.MOrigin, MResolution: inv(s.MScale), MTolerance: s.MTolerance,
	}
	if sr.WKID == 0 {
		sr.WKID = s.WKID
	}
	return sr, nil
}
