// Copyright 2021 Saferwall. All rights reserved.
// Use of this source code is governed by Apache v2 license
// license that can be found in the LICENSE file.

package file

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/saferwall/saferwall-api/internal/entity"
	"github.com/saferwall/saferwall-api/internal/errors"
	"github.com/saferwall/saferwall-api/pkg/log"
	"github.com/saferwall/saferwall-api/pkg/pagination"
)

type resource struct {
	service Service
	logger  log.Logger
}

func RegisterHandlers(g *echo.Group, service Service, logger log.Logger,
	requireLogin, optionalLogin, verifyHash echo.MiddlewareFunc) {

	res := resource{service, logger}

	g.GET("/files/", res.getFiles)
	g.POST("/files/", res.create, requireLogin)

	g.GET("/files/:sha256/", res.get, verifyHash)
	g.PUT("/files/:sha256/", res.update, requireLogin)
	g.PATCH("/files/:sha256/", res.patch, requireLogin)
	g.DELETE("/files/:sha256/", res.delete, requireLogin)

	g.GET("/files/:sha256/strings/", res.strings, verifyHash)
	g.GET("/files/:sha256/summary/", res.summary, verifyHash, optionalLogin)
	g.POST("/files/:sha256/like/", res.like, verifyHash, requireLogin)
	g.POST("/files/:sha256/unlike/", res.unlike, verifyHash, requireLogin)
	g.POST("/files/:sha256/rescan/", res.rescan, verifyHash, requireLogin)
	g.GET("/files/:sha256/download/", res.download, requireLogin)
	g.GET("/files/:sha256/comments/", res.comments, optionalLogin)
}

// get godoc
// @Summary Get file
// @Description Get a file report
// @Tags file
// @Accept json
// @Produce json
// @Param sha256 path string true "File SHA256"
// @Success 200 {object} entity.File
// @Failure 400 {object} errors.ErrorResponse
// @Router /files/{sha256} [get]
func (r resource) get(c echo.Context) error {

	// the `fields` query parameter is used to limit the fields
	// to include in the response.
	var fields []string
	if fieldsParam := c.QueryParam("fields"); fieldsParam != "" {
		fields = strings.Split(fieldsParam, ",")
	}

	if len(fields) > 0 {
		allowed := isFilterAllowed(getStructFields(entity.File{}), fields)
		if !allowed {
			return errors.BadRequest("field not allowed")
		}
	}

	file, err := r.service.Get(c.Request().Context(), c.Param("sha256"), fields)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, file)
}

// create godoc
// @Summary Create a file
// @Description Submit a new file for scanning
// @Tags file,scan
// @Accept mpfd
// @Produce json
// @Success 201 {object} entity.File
// @Failure 400 {object} errors.ErrorResponse
// @Failure 413 {object} errors.ErrorResponse
// @Failure 500 {object} errors.ErrorResponse
// @Router /files/ [post]
func (r resource) create(c echo.Context) error {

	ctx := c.Request().Context()
	f, err := c.FormFile("file")
	if err != nil {
		r.logger.With(ctx).Info(err)
		return errors.BadRequest("missing file in form request")
	}

	if f.Size > 60000000 {
		r.logger.With(ctx).Info("payload too large")
		return errors.TooLargeEntity("")
	}

	src, err := f.Open()
	if err != nil {
		r.logger.With(ctx).Error(err)
		return errors.InternalServerError("")
	}
	defer src.Close()

	r.logger.With(ctx).Debug("Header is: %v", c.Request().Header)

	input := CreateFileRequest{
		src:      src,
		filename: f.Filename,
		geoip:    c.Request().Header.Get("X-Geoip-Country"),
	}
	file, err := r.service.Create(ctx, input)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, file)
}

// update godoc
// @Summary update a file report (full update)
// @Description Replace a file report with a new report
// @Tags file,update
// @Accept json
// @Produce json
// @Param sha256 path string true "File SHA256"
// @Success 200 {object} entity.File
// @Failure 400 {object} errors.ErrorResponse
// @Failure 413 {object} errors.ErrorResponse
// @Failure 500 {object} errors.ErrorResponse
// @Router /files/{sha256} [put]

func (r resource) update(c echo.Context) error {

	var isAdmin bool
	ctx := c.Request().Context()
	if user, ok := ctx.Value(entity.UserKey).(entity.User); ok {
		isAdmin = user.IsAdmin()
	}
	if !isAdmin {
		return errors.Forbidden("")
	}

	var input UpdateFileRequest
	if err := c.Bind(&input); err != nil {
		r.logger.With(c.Request().Context()).Info(err)
		return errors.BadRequest("")
	}

	file, err := r.service.Update(c.Request().Context(),
		c.Param("sha256"), input)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, file)
}

func (r resource) patch(c echo.Context) error {
	var isAdmin bool
	ctx := c.Request().Context()
	if user, ok := ctx.Value(entity.UserKey).(entity.User); ok {
		isAdmin = user.IsAdmin()
	}
	if !isAdmin {
		return errors.Forbidden("")
	}
	return nil
}

func (r resource) delete(c echo.Context) error {

	var isAdmin bool
	ctx := c.Request().Context()
	if user, ok := ctx.Value(entity.UserKey).(entity.User); ok {
		isAdmin = user.IsAdmin()
	}
	if !isAdmin {
		return errors.Forbidden("")
	}

	file, err := r.service.Delete(c.Request().Context(), c.Param("sha256"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, file)
}

func (r resource) getFiles(c echo.Context) error {
	var isAdmin bool
	ctx := c.Request().Context()
	if user, ok := ctx.Value(entity.UserKey).(entity.User); ok {
		isAdmin = user.IsAdmin()
	}
	if !isAdmin {
		return errors.Forbidden("")
	}

	count, err := r.service.Count(ctx)
	if err != nil {
		return err
	}
	pages := pagination.NewFromRequest(c.Request(), count)
	files, err := r.service.Query(ctx, pages.Offset(), pages.Limit())
	if err != nil {
		return err
	}
	pages.Items = files
	return c.JSON(http.StatusOK, pages)
}

func (r resource) strings(c echo.Context) error {
	ctx := c.Request().Context()
	count, err := r.service.CountStrings(ctx, c.Param("sha256"))
	if err != nil {
		return err
	}
	pages := pagination.NewFromRequest(c.Request(), count)
	strings, err := r.service.Strings(
		ctx, c.Param("sha256"), pages.Offset(), pages.Limit())
	if err != nil {
		return err
	}
	pages.Items = strings
	return c.JSON(http.StatusOK, pages)
}

func (r resource) summary(c echo.Context) error {
	ctx := c.Request().Context()
	fileSummary, err := r.service.Summary(ctx, c.Param("sha256"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, fileSummary)
}

func (r resource) like(c echo.Context) error {
	ctx := c.Request().Context()
	err := r.service.Like(ctx, c.Param("sha256"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, struct {
		Message string `json:"message"`
		Status  int    `json:"status"`
	}{"ok", http.StatusOK})
}

func (r resource) unlike(c echo.Context) error {
	ctx := c.Request().Context()
	err := r.service.Unlike(ctx, c.Param("sha256"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, struct {
		Message string `json:"message"`
		Status  int    `json:"status"`
	}{"ok", http.StatusOK})
}

func (r resource) rescan(c echo.Context) error {
	ctx := c.Request().Context()
	err := r.service.Rescan(ctx, c.Param("sha256"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, struct {
		Message string `json:"message"`
		Status  int    `json:"status"`
	}{"ok", http.StatusOK})
}

func (r resource) download(c echo.Context) error {
	ctx := c.Request().Context()
	var zippedFile string
	err := r.service.Download(ctx, c.Param("sha256"), &zippedFile)
	if err != nil {
		switch err {
		case ErrObjectNotFound:
			return errors.NotFound("")
		default:
			return err
		}
	}
	return c.File(zippedFile)
}

func (r resource) comments(c echo.Context) error {
	ctx := c.Request().Context()
	file, err := r.service.Get(ctx, c.Param("sha256"), nil)
	if err != nil {
		return err
	}
	count := file.CommentsCount
	pages := pagination.NewFromRequest(c.Request(), count)
	comments, err := r.service.Comments(
		ctx, c.Param("sha256"), pages.Offset(), pages.Limit())
	if err != nil {
		return err
	}
	pages.Items = comments
	return c.JSON(http.StatusOK, pages)
}
