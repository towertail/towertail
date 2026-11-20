package api

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/wire"
)

// handleDisks returns the use of each mount of a node in a range.
func (s *Server) handleDisks(c echo.Context) error {
	id, err := parseNodeID(c)
	if err != nil {
		return err
	}
	if err := s.requireCH(c); err != nil {
		return err
	}
	t, _ := authctx.TokenFrom(c)
	from, to, _ := rangeBounds(c, 24*time.Hour)
	res, err := s.query.Disks(c.Request().Context(), t.OrgID, id, from, to)
	if err != nil {
		return err
	}
	out := wire.NodeDisksResponse{
		NodeID:     id,
		From:       res.From,
		To:         res.To,
		Resolution: resolution(res.Bucket),
		Clamped:    res.Clamped,
		Mounts:     []wire.DiskSeries{},
	}
	for _, p := range res.Points {
		// The rows come in mount order.
		if n := len(out.Mounts); n == 0 || out.Mounts[n-1].Mount != p.Mount {
			out.Mounts = append(out.Mounts, wire.DiskSeries{Mount: p.Mount, FS: p.FS})
		}
		m := &out.Mounts[len(out.Mounts)-1]
		m.FS = p.FS
		m.Points = append(m.Points, wire.DiskPoint{TS: p.TS, Used: p.Used, Total: p.Total})
	}
	return c.JSON(http.StatusOK, out)
}
