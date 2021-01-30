// Copyright 2020 Saferwall. All rights reserved.
// Use of this source code is governed by Apache v2 license
// license that can be found in the LICENSE file.

package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strings"
	"time"

	"github.com/kjk/betterguid"

	"github.com/couchbase/gocb/v2"
	"github.com/dgrijalva/jwt-go"
	"github.com/labstack/echo/v4"
	"github.com/minio/minio-go/v6"
	"github.com/saferwall/saferwall/pkg/crypto"
	peparser "github.com/saferwall/saferwall/pkg/peparser"
	u "github.com/saferwall/saferwall/pkg/utils"
	"github.com/saferwall/saferwall/web/app"
	"github.com/saferwall/saferwall/web/app/common/db"
	"github.com/saferwall/saferwall/web/app/common/utils"
	"github.com/saferwall/saferwall/web/app/handler/user"
	log "github.com/sirupsen/logrus"
	"github.com/xeipuuv/gojsonschema"
)

const (
	sha256regex = "^[a-f0-9]{64}$"
)

type stringStruct struct {
	Encoding string `json:"encoding"`
	Value    string `json:"value"`
}

type submission struct {
	Date     *time.Time `json:"date,omitempty"`
	Filename string     `json:"filename,omitempty"`
	Source   string     `json:"source,omitempty"`
	Country  string     `json:"country,omitempty"`
}

type Comment struct {
	Timestamp *time.Time `json:"timestamp,omitempty"`
	Username  string     `json:"username,omitempty"`
	Body      string     `json:"body,omitempty"`
	ID        string     `json:"id,omitempty"`
}

// File represent a sample
type File struct {
	Md5             string                 `json:"md5,omitempty"`
	Sha1            string                 `json:"sha1,omitempty"`
	Sha256          string                 `json:"sha256,omitempty"`
	Sha512          string                 `json:"sha512,omitempty"`
	Ssdeep          string                 `json:"ssdeep,omitempty"`
	Crc32           string                 `json:"crc32,omitempty"`
	Magic           string                 `json:"magic,omitempty"`
	Size            int64                  `json:"size,omitempty"`
	Exif            map[string]string      `json:"exif,omitempty"`
	TriD            []string               `json:"trid,omitempty"`
	Tags            map[string]interface{} `json:"tags,omitempty"`
	Packer          []string               `json:"packer,omitempty"`
	FirstSubmission *time.Time             `json:"first_submission,omitempty"`
	LastSubmission  *time.Time             `json:"last_submission,omitempty"`
	LastScanned     *time.Time             `json:"last_scanned,omitempty"`
	Submissions     []submission           `json:"submissions,omitempty"`
	Strings         []stringStruct         `json:"strings,omitempty"`
	MultiAV         map[string]interface{} `json:"multiav,omitempty"`
	Status          int                    `json:"status,omitempty"`
	Comments        []Comment              `json:"comments,omitempty"`
	PE              *peparser.File         `json:"pe,omitempty"`
	Histogram       []int                  `json:"histogram,omitempty"`
	ByteEntropy     []int                  `json:"byte_entropy,omitempty"`
	Type            string                 `json:"type,omitempty"`
}

// Response JSON
type Response struct {
	Sha256      string `json:"sha256,omitempty"`
	Message     string `json:"message,omitempty"`
	Description string `json:"description,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

// AV vendor
type AV struct {
	Vendor string `json:"vendor,omitempty"`
}

const (
	queued     = iota
	processing = iota
	finished   = iota
)

// Save creates a new file
func (file *File) Save() error {
	_, err := db.FilesCollection.Upsert(file.Sha256, file, &gocb.UpsertOptions{})
	if err != nil {
		log.Errorf("FilesCollection.Upsert() failed with: %v", err)
		return err
	}

	log.Infof("File %s added to database.", file.Sha256)
	return nil
}

// GetFileBySHA256 return user document
func GetFileBySHA256(sha256 string) (File, error) {

	file := File{}
	getResult, err := db.FilesCollection.Get(sha256, &gocb.GetOptions{})
	if err != nil {
		log.Errorf("FilesCollection.Get() failed with: %v", err)
		return file, err
	}

	err = getResult.Content(&file)
	if err != nil {
		log.Errorf("getResult.Content() failed with: %v", err)
		return file, err
	}
	return file, nil
}

// GetAllFiles return all files (optional: selecting fields)
func GetAllFiles(fields []string) ([]File, error) {

	// Interfaces for handling streaming return values
	var row File
	var retValues []File

	// Select only demanded fields
	var query string
	if len(fields) > 0 {
		var buffer bytes.Buffer
		buffer.WriteString("SELECT ")
		length := len(fields)
		for index, field := range fields {
			buffer.WriteString(field)
			if index < length-1 {
				buffer.WriteString(",")
			}
		}
		buffer.WriteString(" FROM `files`")
		query = buffer.String()
	} else {
		query = "SELECT files.* FROM `files`"
	}

	// Execute our query
	results, err := db.Cluster.Query(query, &gocb.QueryOptions{})
	if err != nil {
		log.Errorf("Error executing n1ql query: %v", err)
		return retValues, nil
	}

	// Stream the values returned from the query into a typed array of structs
	for results.Next() {
		err := results.Row(&row)
		if err != nil {
			log.Errorf("results.Row() failed with: %v", err)
		}
		retValues = append(retValues, row)
		row = File{}
	}

	return retValues, nil
}

// getByCommentID retreieve a comment from its id.
func (file *File) getByCommentID(commentID string) Comment {
	for _, com := range file.Comments {
		if com.ID == commentID {
			return com
		}
	}
	return Comment{}
}

// DumpRequest dumps the headers.
func DumpRequest(req *http.Request) {
	requestDump, err := httputil.DumpRequest(req, true)
	if err != nil {
		log.Info(err.Error())
	} else {
		log.Info(string(requestDump))
	}

}

// GetFileByFields return user by username(optional: selecting fields)
func GetFileByFields(fields []string, sha256 string) (File, error) {

	// Select only demanded fields
	var query string
	if len(fields) > 0 {
		var buffer bytes.Buffer
		buffer.WriteString("SELECT ")
		length := len(fields)
		for index, field := range fields {
			buffer.WriteString(field)
			if index < length-1 {
				buffer.WriteString(",")
			}
		}
		buffer.WriteString(" FROM `files` WHERE `sha256`=$sha256")
		query = buffer.String()
	} else {
		query = "SELECT files.* FROM `files` WHERE `sha256`=$sha256"
	}

	// Execute Query
	params := make(map[string]interface{}, 1)
	params["sha256"] = sha256
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params, Adhoc: true})
	if err != nil {
		log.Errorf("Cluster.Query() failed with: %v", err)
		return File{}, err
	}

	// Interfaces for handling streaming return values.
	var row File

	// Stream the first result only into the interface.
	err = results.One(&row)
	if err != nil {
		log.Errorf("results.One() failed with: %v", err)
		return row, err
	}

	return row, nil
}

//=================== /file/sha256 handlers ===================

// GetFile returns file informations.
func GetFile(c echo.Context) error {

	// get path param
	sha256 := strings.ToLower(c.Param("sha256"))
	matched, _ := regexp.MatchString(sha256regex, sha256)
	if !matched {
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Invalid sha265",
			Description: "Invalid hash submitted: " + sha256,
		})
	}

	// get query param `fields` for filtering & sanitize them
	filters := utils.GetQueryParamsFields(c)
	if len(filters) > 0 {
		allowed := utils.IsFilterAllowed(utils.GetStructFields(File{}), filters)
		if !allowed {
			return c.JSON(http.StatusBadRequest, "Filters not allowed")
		}

		// get path param
		file, err := GetFileByFields(filters, sha256)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]string{
				"verbose_msg": "File not found"})
		}
		return c.JSON(http.StatusOK, file)
	}

	// no field filters.
	file, err := GetFileBySHA256(sha256)
	if err != nil {
		if errors.Is(err, gocb.ErrDocumentNotFound) {
			return c.JSON(http.StatusNotFound, Response{
				Message:     err.Error(),
				Description: "File was not found in our database",
				Sha256:      sha256,
			})
		}

		return c.JSON(http.StatusInternalServerError, Response{
			Message: err.Error(),
			Sha256:  sha256,
		})
	}

	return c.JSON(http.StatusOK, file)
}

// PutFile updates a specific file
func PutFile(c echo.Context) error {

	// get path param
	sha256 := strings.ToLower(c.Param("sha256"))

	// Read the json body
	b, err := ioutil.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	// Validate JSON
	l := gojsonschema.NewBytesLoader(b)
	result, err := app.FileSchema.Validate(l)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}
	if !result.Valid() {
		msg := ""
		for _, desc := range result.Errors() {
			msg += fmt.Sprintf("%s, ", desc.Description())
		}
		msg = strings.TrimSuffix(msg, ", ")
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": msg})
	}

	matched, _ := regexp.MatchString(sha256regex, sha256)
	if !matched {
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Invalid sha265",
			Description: "File hash is not a sha256 hash" + sha256,
			Sha256:      sha256,
		})
	}

	// Get the document.
	file, err := GetFileBySHA256(sha256)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	// merge it
	err = json.Unmarshal(b, &file)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	// Specific checks
	_, alreadyScanned := file.MultiAV["last_scan"]
	if alreadyScanned {
		_, ok := file.MultiAV["first_scan"]
		if !ok {
			file.MultiAV["first_scan"] = file.MultiAV["last_scan"]
		}
	}

	db.FilesCollection.Upsert(sha256, file, &gocb.UpsertOptions{})
	return c.JSON(http.StatusOK, sha256)
}

// DeleteFile deletes a specific file
func DeleteFile(c echo.Context) error {

	// get path param
	sha256 := strings.ToLower(c.Param("sha256"))
	return c.JSON(http.StatusOK, sha256)
}

// deleteAllFiles will empty files bucket
func deleteAllFiles() {
	// Keep in mind that you must have flushing enabled in the buckets configuration.
	mgr := db.Cluster.Buckets()
	err := mgr.FlushBucket("files", nil)
	if err != nil {
		log.Errorf("Failed to flush bucket manager %v", err)
	}
}

// GetFiles returns list of files.
func GetFiles(c echo.Context) error {
	// get query param `fields` for filtering & sanitize them
	filters := utils.GetQueryParamsFields(c)
	if len(filters) > 0 {
		file := File{}
		allowed := utils.IsFilterAllowed(utils.GetStructFields(file), filters)
		if !allowed {
			return c.JSON(http.StatusBadRequest, "Filters not allowed")
		}
	}

	// get all users
	allFiles, err := GetAllFiles(filters)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, allFiles)
}

// PostFiles creates a new file
func PostFiles(c echo.Context) error {
	userToken := c.Get("user").(*jwt.Token)
	claims := userToken.Claims.(jwt.MapClaims)
	username := claims["name"].(string)

	// Get user infos.
	usr, err := user.GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exists"})
	}

	// Source
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Missing file",
			Description: "Did you send the file via the form request ?",
		})
	}

	// Check file size
	if fileHeader.Size > app.MaxFileSize {
		return c.JSON(http.StatusRequestEntityTooLarge, Response{
			Message:     "File too large",
			Description: "The maximum allowed is 64MB",
			Filename:    fileHeader.Filename,
		})
	}

	// Open the file
	file, err := fileHeader.Open()
	if err != nil {
		log.Errorf("fileHeader.Open() failed with: %v", err)
		return c.JSON(http.StatusInternalServerError, Response{
			Message:     "Failed to open the file",
			Description: "Internal error",
			Filename:    fileHeader.Filename,
		})
	}
	defer file.Close()

	// Get the size
	size := fileHeader.Size

	// Read the content
	fileContents, err := ioutil.ReadAll(file)
	if err != nil {
		log.Errorf("ioutil.ReadAll() failed with: %v", err)
		return c.JSON(http.StatusInternalServerError, Response{
			Message:     "Failed to read the file content",
			Description: err.Error(),
			Filename:    fileHeader.Filename,
		})
	}

	sha256 := crypto.GetSha256(fileContents)

	// Have we seen this file before
	fileDocument, err := GetFileBySHA256(sha256)
	if err != nil && !errors.Is(err, gocb.ErrDocumentNotFound) {
		return c.JSON(http.StatusInternalServerError, Response{
			Message:     "Something unexpected happened",
			Description: err.Error(),
			Filename:    fileHeader.Filename,
		})
	}

	if errors.Is(err, gocb.ErrDocumentNotFound) {
		// Upload the sample to the object storage.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n, err := app.MinioClient.PutObjectWithContext(ctx, app.SamplesSpaceBucket,
			sha256, bytes.NewReader(fileContents), size,
			minio.PutObjectOptions{ContentType: "application/octet-stream"})
		if err != nil {
			log.Errorf("PutObjectOptions() failed with: %v", err)
			return c.JSON(http.StatusInternalServerError, Response{
				Message:     "PutObject failed",
				Description: err.Error(),
				Filename:    fileHeader.Filename,
				Sha256:      sha256,
			})
		}
		log.Debugf("Successfully uploaded bytes: %d", n)

		// Save to DB
		now := time.Now().UTC()
		newFile := File{
			Sha256:          sha256,
			FirstSubmission: &now,
			LastSubmission:  &now,
			Size:            fileHeader.Size,
			Status:          queued,
		}

		// Create new submission
		s := submission{
			Date:     &now,
			Filename: fileHeader.Filename,
			Source:   "web",
			Country:  c.Request().Header.Get("X-Geoip-Country"),
		}
		newFile.Submissions = append(newFile.Submissions, s)
		newFile.Save()

		usr.Submissions = append(usr.Submissions, user.Submission{
			Timestamp: &now, Sha256: sha256})
		usr.Save()

		// Push it to NSQ
		err = app.NsqProducer.Publish("scan", []byte(sha256))
		if err != nil {
			log.Errorf("NsqProducer.Publish() failed with: %v", err)
			return c.JSON(http.StatusInternalServerError, Response{
				Message:     "Publish failed",
				Description: err.Error(),
				Filename:    fileHeader.Filename,
				Sha256:      sha256,
			})
		}

		// add new activity
		activity := usr.NewActivity("submit", map[string]string{
			"sha256": sha256})
		usr.Activities = append(usr.Activities, activity)
		usr.Save()

		// All went fine
		return c.JSON(http.StatusCreated, Response{
			Sha256:      sha256,
			Message:     "ok",
			Description: "File queued successfully for analysis",
			Filename:    fileHeader.Filename,
		})
	}

	// We have already seen this file, create new submission
	now := time.Now().UTC()
	s := submission{
		Date:     &now,
		Filename: fileHeader.Filename,
		Source:   "api",
		Country:  c.Request().Header.Get("X-Geoip-Country"),
	}
	fileDocument.Submissions = append(fileDocument.Submissions, s)
	fileDocument.LastSubmission = &now
	fileDocument.Save()

	return c.JSON(http.StatusOK, fileDocument)
}

// PutFiles bulk updates of files
func PutFiles(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"verbose_msg": "not implemented"})
}

// DeleteFiles delete all files
func DeleteFiles(c echo.Context) error {
	go deleteAllFiles()
	return c.JSON(http.StatusOK, map[string]string{
		"verbose_msg": "ok"})
}

// Download downloads a file.
func Download(c echo.Context) error {
	// get path param
	sha256 := strings.ToLower(c.Param("sha256"))

	reader, err := app.MinioClient.GetObject(
		app.SamplesSpaceBucket, sha256, minio.GetObjectOptions{})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	defer reader.Close()

	_, err = reader.Stat()
	if err != nil {
		return c.JSON(http.StatusNotFound, err.Error())
	}

	filepath, err := u.ZipEncrypt(sha256, "infected", reader)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}
	return c.File(filepath)
}

// Actions over a file. Rescan or Download.
func Actions(c echo.Context) error {

	// get sha256 param.
	sha256 := strings.ToLower(c.Param("sha256"))
	matched, _ := regexp.MatchString(sha256regex, sha256)
	if !matched {
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Invalid sha256",
			Description: "File hash is not a sha256 hash",
			Sha256:      sha256,
		})
	}

	// Read the json body.
	b, err := ioutil.ReadAll(c.Request().Body)
	if err != nil {
		log.Errorf("ioutil.ReadAll() failed with: %v", err)
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Failed to read the file",
			Description: err.Error(),
			Sha256:      sha256,
		})
	}

	// Verify length
	if len(b) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "You have sent an empty json"})
	}

	// Validate JSON
	l := gojsonschema.NewBytesLoader(b)
	result, err := app.FileActionSchema.Validate(l)
	if err != nil {
		log.Errorf("FileActionSchema.Validate() failed with: %v", err)
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Failed to validate file schema",
			Description: err.Error(),
			Sha256:      sha256,
		})
	}

	if !result.Valid() {
		msg := ""
		for _, desc := range result.Errors() {
			msg += fmt.Sprintf("%s, ", desc.Description())
		}
		msg = strings.TrimSuffix(msg, ", ")
		log.Debugf("result.Valid() failed with: %s", msg)
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Failed to validate file schema",
			Description: msg,
			Sha256:      sha256,
		})
	}

	// get the type of action
	var actions map[string]interface{}
	err = json.Unmarshal(b, &actions)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	actionType := actions["type"].(string)
	_, err = GetFileBySHA256(sha256)
	if err != nil {
		if errors.Is(err, gocb.ErrDocumentNotFound) {
			return c.JSON(http.StatusNotFound, Response{
				Message:     err.Error(),
				Description: "File was not found in our database",
				Sha256:      sha256,
			})
		}
		log.Errorf("GetFileBySHA256() failed with: %v", err)
		return c.JSON(http.StatusInternalServerError, Response{
			Message:     "Something unexpected happened",
			Description: err.Error(),
			Sha256:      sha256,
		})
	}

	switch actionType {
	case "rescan":
		// Push it to NSQ
		err = app.NsqProducer.Publish("scan", []byte(sha256))
		if err != nil {
			log.Errorf("NsqProducer.Publish() failed with: %v", err)
			return c.JSON(http.StatusInternalServerError, Response{
				Message:     "Publish failed",
				Description: "Internal error",
				Sha256:      sha256,
			})
		}
		return c.JSON(http.StatusOK, Response{
			Message:     "File rescan successful",
			Description: "Type of action: " + actionType,
			Sha256:      sha256,
		})
	case "download":
		reader, err := app.MinioClient.GetObject(
			app.SamplesSpaceBucket, sha256, minio.GetObjectOptions{})
		if err != nil {
			return c.JSON(http.StatusInternalServerError, err.Error())
		}
		defer reader.Close()

		_, err = reader.Stat()
		if err != nil {
			return c.JSON(http.StatusNotFound, err.Error())
		}

		filepath, err := u.ZipEncrypt(sha256, "infected", reader)
		if err != nil {
			return c.JSON(http.StatusBadRequest, err.Error())
		}
		return c.File(filepath)
	case "like":
		// extract user from token
		u := c.Get("user").(*jwt.Token)
		claims := u.Claims.(jwt.MapClaims)
		username := claims["name"].(string)

		// Get user infos.
		usr, err := user.GetByUsername(username)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"verbose_msg": "Username does not exist"})
		}

		if !utils.IsStringInSlice(sha256, usr.Likes) {
			usr.Likes = append(usr.Likes, sha256)

			// add new activity
			activity := usr.NewActivity("like", map[string]string{
				"sha256": sha256})
			usr.Activities = append(usr.Activities, activity)
			usr.Save()
		}

		return c.JSON(http.StatusOK, map[string]string{
			"verbose_msg": "Sample has been liked successefuly"})
	case "unlike":
		// extract user from token
		u := c.Get("user").(*jwt.Token)
		claims := u.Claims.(jwt.MapClaims)
		username := claims["name"].(string)

		// Get user infos.
		usr, err := user.GetByUsername(username)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"verbose_msg": "Username does not exist"})
		}

		if utils.IsStringInSlice(sha256, usr.Likes) {
			usr.Likes = utils.RemoveStringFromSlice(usr.Likes, sha256)
			usr.Save()
		}

		return c.JSON(http.StatusOK, map[string]string{
			"verbose_msg": "Sample has been unliked successefuly"})
	}

	return c.JSON(http.StatusInternalServerError, Response{
		Message:     "Unknown action",
		Description: "Type of action: " + actionType,
		Sha256:      sha256,
	})
}

// PostComment creates a new comment.
func PostComment(c echo.Context) error {

	// get path param
	sha256 := strings.ToLower(c.Param("sha256"))

	// Read the json body
	b, err := ioutil.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	// Validate JSON
	l := gojsonschema.NewBytesLoader(b)
	result, err := app.CommentSchema.Validate(l)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}
	if !result.Valid() {
		msg := ""
		for _, desc := range result.Errors() {
			msg += fmt.Sprintf("%s, ", desc.Description())
		}
		msg = strings.TrimSuffix(msg, ", ")
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": msg})
	}

	matched, _ := regexp.MatchString(sha256regex, sha256)
	if !matched {
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Invalid sha265",
			Description: "File hash is not a sha256 hash" + sha256,
			Sha256:      sha256,
		})
	}

	// Get the document.
	file, err := GetFileBySHA256(sha256)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	// Get the user
	currentUser := c.Get("user").(*jwt.Token)
	claims := currentUser.Claims.(jwt.MapClaims)
	username := claims["name"].(string)

	usr, err := user.GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exist"})
	}

	// Create a new comment to store in file document.
	com := Comment{}
	err = json.Unmarshal(b, &com)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	// Overwrite the content for now
	now := time.Now().UTC()
	commentID := betterguid.New()

	com.Timestamp = &now
	com.Username = username
	com.ID = commentID
	file.Comments = append(file.Comments, com)
	file.Save()

	// Create the same comment to store in user document.
	userCom := user.Comment{}
	userCom.Timestamp = &now
	userCom.ID = commentID
	userCom.Body = com.Body
	userCom.Sha256 = sha256
	usr.Comments = append(usr.Comments, userCom)
	usr.Save()

	// add new activity
	activity := usr.NewActivity("comment", map[string]string{
		"sha256": sha256, "body": com.Body})
	usr.Activities = append(usr.Activities, activity)
	usr.Save()

	return c.JSON(http.StatusOK, com)
}

// DeleteComment deletes a comment.
func DeleteComment(c echo.Context) error {

	// Get the file doc.
	sha256 := strings.ToLower(c.Param("sha256"))
	file, err := GetFileBySHA256(sha256)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	// Get the user
	currentUser := c.Get("user").(*jwt.Token)
	claims := currentUser.Claims.(jwt.MapClaims)
	currentUsername := claims["name"].(string)

	// Get comment
	commentID := c.Param("id")
	com := file.getByCommentID(commentID)

	if (Comment{} == com) {
		return c.JSON(http.StatusNotFound, map[string]string{
			"verbose_msg": "Comment not found"})
	}

	// Check if user own comment
	if com.Username != currentUsername {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"verbose_msg": "Not allowed to delete someone else comment"})
	}

	// Delete comment.
	for i, com := range file.Comments {
		if com.ID == commentID {
			file.Comments = append(file.Comments[:i], file.Comments[i+1:]...)
			file.Save()
			break
		}
	}

	return c.JSON(http.StatusOK, map[string]string{
		"verbose_msg": "Comment was deleted"})
}

// ScanFileFromObjectStorage scans which was pushed to object
// storage directly.
func ScanFileFromObjectStorage(c echo.Context) error {

	sha256 := strings.ToLower(c.Param("sha256"))
	matched, err := regexp.MatchString(sha256regex, sha256)
	if !matched {
		return c.JSON(http.StatusBadRequest, Response{
			Message:     "Invalid sha256",
			Description: err.Error(),
			Sha256:      sha256,
		})
	}

	// Save to DB
	now := time.Now().UTC()
	newFile := File{
		Sha256:          sha256,
		FirstSubmission: &now,
		LastSubmission:  &now,
		Status:          queued,
	}

	// Create new submission
	s := submission{
		Date:     &now,
		Filename: sha256,
		Source:   "api",
		Country:  c.Request().Header.Get("X-Geoip-Country"),
	}
	newFile.Submissions = append(newFile.Submissions, s)
	newFile.Save()

	// Push it to NSQ
	err = app.NsqProducer.Publish("scan", []byte(sha256))
	if err != nil {
		log.Errorf("NsqProducer.Publish() failed with: %v", err)
		return c.JSON(http.StatusInternalServerError, Response{
			Message:     "Failed to publish to NSQ",
			Description: err.Error(),
			Sha256:      sha256,
		})
	}

	return c.JSON(http.StatusCreated, Response{
		Message:     "ok",
		Description: "File queued successfully for analysis",
		Sha256:      sha256,
	})
}
