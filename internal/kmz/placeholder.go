package kmz

import (
	"archive/zip"
	"bytes"
	"fmt"
	"time"
)

// PlaceholderKMZ produces a minimal valid DJI Fly waypoint KMZ
// containing a single waypoint at the given coordinates. Used by
// ClearSlot to overwrite an existing mission with an obviously-empty
// placeholder rather than leaving stale flight data in the slot.
//
// We model the output on the format DJI Fly itself writes when it
// first creates a placeholder slot (we pulled an example off the live
// device while building this — the 825-byte template.kml + 6 KB
// waylines.wpml shape). Same XML namespace, same field set, same
// drone enum values.
func PlaceholderKMZ(lat, lng float64) ([]byte, error) {
	nowMS := time.Now().UnixMilli()
	template := fmt.Sprintf(placeholderTemplateKML, nowMS, nowMS)
	waylines := fmt.Sprintf(placeholderWaylinesWPML, nowMS, nowMS, lng, lat)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("wpmz/template.kml")
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(template)); err != nil {
		return nil, err
	}
	w, err = zw.Create("wpmz/waylines.wpml")
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(waylines)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const placeholderTemplateKML = `<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2" xmlns:wpml="http://www.dji.com/wpmz/1.0.2">
  <Document>
    <wpml:author>kam-mission</wpml:author>
    <wpml:createTime>%d</wpml:createTime>
    <wpml:updateTime>%d</wpml:updateTime>
    <wpml:missionConfig>
      <wpml:flyToWaylineMode>safely</wpml:flyToWaylineMode>
      <wpml:finishAction>goHome</wpml:finishAction>
      <wpml:exitOnRCLost>executeLostAction</wpml:exitOnRCLost>
      <wpml:executeRCLostAction>goBack</wpml:executeRCLostAction>
      <wpml:takeOffSecurityHeight>30</wpml:takeOffSecurityHeight>
      <wpml:globalTransitionalSpeed>2.5</wpml:globalTransitionalSpeed>
      <wpml:droneInfo>
        <wpml:droneEnumValue>68</wpml:droneEnumValue>
        <wpml:droneSubEnumValue>0</wpml:droneSubEnumValue>
      </wpml:droneInfo>
    </wpml:missionConfig>
  </Document>
</kml>`

const placeholderWaylinesWPML = `<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2" xmlns:wpml="http://www.dji.com/wpmz/1.0.2">
  <Document>
    <wpml:author>kam-mission</wpml:author>
    <wpml:createTime>%d</wpml:createTime>
    <wpml:updateTime>%d</wpml:updateTime>
    <wpml:missionConfig>
      <wpml:flyToWaylineMode>safely</wpml:flyToWaylineMode>
      <wpml:finishAction>goHome</wpml:finishAction>
      <wpml:exitOnRCLost>executeLostAction</wpml:exitOnRCLost>
      <wpml:executeRCLostAction>goBack</wpml:executeRCLostAction>
      <wpml:takeOffSecurityHeight>30</wpml:takeOffSecurityHeight>
      <wpml:globalTransitionalSpeed>2.5</wpml:globalTransitionalSpeed>
      <wpml:droneInfo>
        <wpml:droneEnumValue>68</wpml:droneEnumValue>
        <wpml:droneSubEnumValue>0</wpml:droneSubEnumValue>
      </wpml:droneInfo>
    </wpml:missionConfig>
    <Folder>
      <wpml:templateId>0</wpml:templateId>
      <wpml:executeHeightMode>relativeToStartPoint</wpml:executeHeightMode>
      <wpml:waylineId>0</wpml:waylineId>
      <wpml:autoFlightSpeed>2.5</wpml:autoFlightSpeed>
      <Placemark>
        <Point>
          <coordinates>%.7f,%.7f</coordinates>
        </Point>
        <wpml:index>0</wpml:index>
        <wpml:executeHeight>10</wpml:executeHeight>
        <wpml:waypointSpeed>2.5</wpml:waypointSpeed>
        <wpml:waypointHeadingParam>
          <wpml:waypointHeadingMode>followWayline</wpml:waypointHeadingMode>
          <wpml:waypointPoiPoint>0.000000,0.000000,0.000000</wpml:waypointPoiPoint>
          <wpml:waypointHeadingAngleEnable>0</wpml:waypointHeadingAngleEnable>
          <wpml:waypointHeadingPathMode>followBadArc</wpml:waypointHeadingPathMode>
        </wpml:waypointHeadingParam>
        <wpml:waypointTurnParam>
          <wpml:waypointTurnMode>toPointAndStopWithDiscontinuityCurvature</wpml:waypointTurnMode>
          <wpml:waypointTurnDampingDist>0</wpml:waypointTurnDampingDist>
        </wpml:waypointTurnParam>
        <wpml:useStraightLine>0</wpml:useStraightLine>
      </Placemark>
    </Folder>
  </Document>
</kml>`
