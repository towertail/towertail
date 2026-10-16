package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/internal/scan"
	"github.com/towertail/towertail/server/internal/store"
	"github.com/towertail/towertail/server/internal/wire"
)

// maxProgressBytes is the limit of one progress post.
const maxProgressBytes = 1 << 20

func (s *Server) handleRequestDiskScan(c echo.Context) error {
	t, _ := tokenFromCtx(c)
	ctx := c.Request().Context()
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	node, err := s.store.GetNode(ctx, t.OrgID, nodeID)
	if err != nil {
		return err
	}
	if !node.AllowDiskScan {
		return echo.NewHTTPError(http.StatusForbidden, "disk scans are off for this node")
	}
	var req wire.DiskScanRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	job, err := s.scans.Request(ctx, t.OrgID, nodeID, req)
	switch {
	case errors.Is(err, scan.ErrActive):
		w := job.Wire()
		return c.JSON(http.StatusConflict, wire.APIErrorEnvelope{
			Error: wire.APIError{
				Code:      "conflict",
				Message:   "the node has an active disk scan",
				RequestID: c.Response().Header().Get(echo.HeaderXRequestID),
			},
			Job: &w,
		})
	case err != nil:
		return scanError(err)
	}
	s.audit(c, "disk_scan_request", "node", nodeID, "job_id", job.ID, "root", job.Root)
	return c.JSON(http.StatusAccepted, job.Wire())
}

func (s *Server) handleListDiskScans(c echo.Context) error {
	t, _ := tokenFromCtx(c)
	ctx := c.Request().Context()
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	if _, err := s.store.GetNode(ctx, t.OrgID, nodeID); err != nil {
		return err
	}
	jobs, err := s.scans.List(ctx, t.OrgID, nodeID)
	if err != nil {
		return err
	}
	out := wire.DiskScanJobList{Jobs: make([]wire.DiskScanJob, 0, len(jobs))}
	for _, j := range jobs {
		out.Jobs = append(out.Jobs, j.Wire())
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) handleGetDiskScan(c echo.Context) error {
	job, err := s.userJob(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, job.Wire())
}

func (s *Server) handleGetDiskScanResult(c echo.Context) error {
	job, err := s.userJob(c)
	if err != nil {
		return err
	}
	gz, err := s.scans.Result(c.Request().Context(), job.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set(echo.HeaderContentEncoding, "gzip")
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSON, gz)
}

func (s *Server) handleCancelDiskScan(c echo.Context) error {
	job, err := s.userJob(c)
	if err != nil {
		return err
	}
	job, err = s.scans.Cancel(c.Request().Context(), job.ID)
	if err != nil {
		return scanError(err)
	}
	s.audit(c, "disk_scan_cancel", "disk_scan", job.ID, "node_id", job.NodeID, "state", job.State)
	return c.JSON(http.StatusOK, job.Wire())
}

func (s *Server) handleDiskScanProgress(c echo.Context) error {
	job, err := s.samplerJob(c)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Response().Writer, c.Request().Body, maxProgressBytes))
	if err != nil {
		return bodyError(err)
	}
	cancel, err := s.scans.Progress(c.Request().Context(), job.ID, body)
	if err != nil {
		return scanError(err)
	}
	return c.JSON(http.StatusOK, wire.DiskScanProgressResponse{Cancel: cancel})
}

func (s *Server) handlePutDiskScanResult(c echo.Context) error {
	job, err := s.samplerJob(c)
	if err != nil {
		return err
	}
	gz, err := io.ReadAll(http.MaxBytesReader(c.Response().Writer, c.Request().Body, scan.MaxResultBytes))
	if err != nil {
		return bodyError(err)
	}
	if _, err := s.scans.SaveResult(c.Request().Context(), job.ID, gz); err != nil {
		return scanError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// userJob loads the job of the path for a user token. A job of another
// org gives 404.
func (s *Server) userJob(c echo.Context) (scan.Job, error) {
	t, _ := tokenFromCtx(c)
	id, err := uuid.Parse(c.Param("job"))
	if err != nil {
		return scan.Job{}, echo.NewHTTPError(http.StatusBadRequest, "bad job id")
	}
	job, err := s.scans.Get(c.Request().Context(), id)
	if err != nil {
		return job, err
	}
	if job.OrgID != t.OrgID {
		return scan.Job{}, store.ErrNotFound
	}
	return job, nil
}

// samplerJob loads the job of the path for a sampler token. A job of
// another node gives 403.
func (s *Server) samplerJob(c echo.Context) (scan.Job, error) {
	t, _ := tokenFromCtx(c)
	id, err := uuid.Parse(c.Param("job"))
	if err != nil {
		return scan.Job{}, echo.NewHTTPError(http.StatusBadRequest, "bad job id")
	}
	job, err := s.scans.Get(c.Request().Context(), id)
	if err != nil {
		return job, err
	}
	if t.NodeID == nil || *t.NodeID != job.NodeID || t.OrgID != job.OrgID {
		return scan.Job{}, echo.NewHTTPError(http.StatusForbidden, "the job is not for the node of this token")
	}
	return job, nil
}

// scanError maps the errors of the scan package to HTTP errors.
func scanError(err error) error {
	switch {
	case errors.Is(err, scan.ErrBadInput), errors.Is(err, scan.ErrTransition):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, scan.ErrTooLarge):
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, scan.ErrFinal):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	}
	return err
}

func bodyError(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "body too large")
	}
	return echo.NewHTTPError(http.StatusBadRequest, err.Error())
}
