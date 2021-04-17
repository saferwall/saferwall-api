// Copyright 2020 Saferwall. All rights reserved.
// Use of this source code is governed by Apache v2 license
// license that can be found in the LICENSE file.

package user

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/couchbase/gocb/v2"
	"github.com/dgrijalva/jwt-go"
	"github.com/labstack/echo/v4"
	"github.com/minio/minio-go/v7"
	"github.com/saferwall/saferwall-api/app"
	"github.com/saferwall/saferwall-api/app/common/db"
	"github.com/saferwall/saferwall-api/app/common/utils"
	"github.com/saferwall/saferwall-api/app/email"
	"github.com/xeipuuv/gojsonschema"

	"bytes"
	"errors"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"

	"github.com/saferwall/saferwall-api/app/middleware"
	"golang.org/x/crypto/bcrypt"
)

var (
	// ErrUserAlreadyConfirmed is retgurned when a user account has been already confirmed.
	ErrUserAlreadyConfirmed = errors.New("Account already confirmed")
)

// Activity represents an event made by the user such as `upload`.
type Activity struct {
	Timestamp *time.Time  `json:"timestamp,omitempty"`
	Type      string      `json:"type,omitempty"`
	Content   interface{} `json:"content,omitempty"`
}

type Submission struct {
	Timestamp *time.Time `json:"timestamp,omitempty"`
	Sha256    string     `json:"sha256,omitempty"`
}

type Comment struct {
	Timestamp *time.Time `json:"timestamp,omitempty"`
	Sha256    string     `json:"sha256,omitempty"`
	Body      string     `json:"body,omitempty"`
	ID        string     `json:"id,omitempty"`
}

// User represent a user.
type User struct {
	Email            string       `json:"email,omitempty"`
	Username         string       `json:"username,omitempty"`
	Password         string       `json:"password,omitempty"`
	Name             string       `json:"name,omitempty"`
	Location         string       `json:"location,omitempty"`
	URL              string       `json:"url,omitempty"`
	Bio              string       `json:"bio,omitempty"`
	Confirmed        bool         `json:"confirmed,omitempty"`
	MemberSince      *time.Time   `json:"member_since,omitempty"`
	LastSeen         *time.Time   `json:"last_seen,omitempty"`
	Admin            bool         `json:"admin,omitempty"`
	HasAvatar        bool         `json:"has_avatar,omitempty"`
	Following        []string     `json:"following,omitempty"`
	FollowingCount   int          `json:"following_count"`
	Followers        []string     `json:"followers,omitempty"`
	FollowersCount   int          `json:"followers_count"`
	Likes            []string     `json:"likes,omitempty"`
	LikesCount       int          `json:"likes_count"`
	Activities       []Activity   `json:"activities,omitempty"`
	Submissions      []Submission `json:"submissions"`
	SubmissionsCount int          `json:"submissions_count"`
	Comments         []Comment    `json:"comments,omitempty"`
	CommentsCount    int          `json:"comments_count"`
}

// NewActivity creates a new activity.
func (u *User) NewActivity(activityType string, content map[string]string) Activity {
	act := Activity{}
	now := time.Now().UTC()
	act.Timestamp = &now
	act.Type = activityType
	act.Content = content
	return act
}

// UpdatePassword creates a JWT token for email confirmation.
func (u *User) UpdatePassword(newPassword string) {
	u.Password = HashAndSalt([]byte(newPassword))

	// Creates the new user and save it to DB.
	u.Save()
}

// GenerateEmailConfirmationToken creates a JWT token for email confirmation.
func (u *User) GenerateEmailConfirmationToken() (string, error) {

	// Set custom claims
	claims := &middleware.CustomClaims{
		u.Username,
		"confirm-email",
		jwt.StandardClaims{
			ExpiresAt: time.Now().Add(time.Hour * 1).Unix(),
		},
	}

	// Create token with claims
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Generate encoded token and send it as response.
	key := viper.GetString("auth.signkey")
	return token.SignedString([]byte(key))
}

// GenerateResetPasswordToken creates a JWT token for password change.
func (u *User) GenerateResetPasswordToken() (string, error) {

	// Set custom claims
	claims := &middleware.CustomClaims{
		u.Username,
		"reset-password",
		jwt.StandardClaims{
			ExpiresAt: time.Now().Add(time.Hour * 1).Unix(),
		},
	}

	// Create token with claims
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Generate encoded token and send it as response.
	key := viper.GetString("auth.signkey")
	return token.SignedString([]byte(key))
}

// HashAndSalt hash with a salt a password.
func HashAndSalt(pwd []byte) string {

	// Use GenerateFromPassword to hash & salt pwd.
	// MinCost is just an integer constant provided by the bcrypt
	// package along with DefaultCost & MaxCost.
	// The cost can be any value you want provided it isn't lower
	// than the MinCost (4)
	hash, err := bcrypt.GenerateFromPassword(pwd, bcrypt.MinCost)
	if err != nil {
		log.Errorf("bcrypt.GenerateFromPassword() failed with: %v", err)
	}

	// GenerateFromPassword returns a byte slice so we need to
	// convert the bytes to a string and return it
	return string(hash)
}

// Save adds user to a database.
func (u *User) Save() {
	_, err := db.UsersCollection.Upsert(strings.ToLower(u.Username), u,
		&gocb.UpsertOptions{})
	if err != nil {
		log.Errorf("UsersCollection.Upsert() failed with: %v", err)
		return
	}
	log.Infof("User %s was saved successefuly", u.Username)
}

// Create creates a new user
func Create(username, password, email string) User {

	t := time.Now().UTC()
	u := User{
		Username:    username,
		Password:    password,
		Email:       email,
		MemberSince: &t,
		Admin:       false,
	}

	return u
}

// Confirm confirms user.
func Confirm(username string) error {
	user, err := GetByUsername(username)
	if err != nil {
		return err
	}

	if user.Confirmed {
		return ErrUserAlreadyConfirmed
	}

	user.Confirmed = true
	user.Save()
	return nil
}

// CheckEmailExist returns true if emails exists
func CheckEmailExist(email string) (bool, error) {

	email = strings.ToLower(email)
	query := "SELECT COUNT(*) as count FROM `users` WHERE `email`=$email;"
	params := make(map[string]interface{}, 1)
	params["email"] = email

	// Execute Query
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params})
	if err != nil {
		log.Errorf("Cluster.Query() failed with: %v", err)
		return false, err
	}
	defer results.Close()

	// Interfaces for handling streaming return values.
	var row interface{}

	// Stream the first result only into the interface
	err = results.One(&row)
	if err != nil {
		log.Errorf("results.One() failed with: %v", err)
		return false, err
	}

	count := row.(map[string]interface{})["count"]
	emailExist := count.(float64) > 0
	return emailExist, nil
}

// GetUserByUsernameFields return user by username(optional: selecting fields)
func GetUserByUsernameFields(fields []string, username string) (User, error) {

	// lower the username.
	username = strings.ToLower(username)

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
		buffer.WriteString(" FROM `users` USE KEYS $username")
		query = buffer.String()
	} else {
		query = "SELECT users.* FROM `users` USE KEYS $username"
	}

	// Execute Query
	params := make(map[string]interface{}, 1)
	params["username"] = username
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params})
	if err != nil {
		log.Errorf("Cluster.Query() failed with: %v", err)
		return User{}, err
	}

	// Interfaces for handling streaming return values.
	var row User

	// Stream the first result only into the interface
	err = results.One(&row)
	if err != nil {
		log.Errorf("results.One() failed with: %v", err)
		return row, err
	}

	return row, nil
}

// DeleteAllUsers will empty users bucket
func DeleteAllUsers() {
	// Keep in mind that you must have flushing enabled in the buckets configuration.
	mgr := db.Cluster.Buckets()
	err := mgr.FlushBucket("users", nil)
	if err != nil {
		log.Errorf("Failed to flush bucket manager %v", err)
	}
}

// GetAllUsers return all users (optional: selecting fields)
func GetAllUsers(fields []string) ([]User, error) {

	// Interfaces for handling streaming return values
	var row User
	var retValues []User

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
		buffer.WriteString(" FROM `users`")
		query = buffer.String()
	} else {
		query = "SELECT users.* FROM `users`"
	}

	// Execute Query
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
		row.Password = ""
		retValues = append(retValues, row)
		row = User{}
	}

	return retValues, nil
}

// GetByUsername return user document
func GetByUsername(username string) (User, error) {

	// get our user
	user := User{}

	username = strings.ToLower(username)
	getResult, err := db.UsersCollection.Get(username, &gocb.GetOptions{})
	if err != nil {
		log.Errorf("UsersCollection.Get() failed with: %v", err)
		return User{}, err
	}

	err = getResult.Content(&user)
	if err != nil {
		log.Errorf("getResult.Content() failed with: %v", err)
		return user, err
	}

	return user, nil
}

// GetUserByEmail return a user document from email
func GetUserByEmail(email string) (User, error) {

	email = strings.ToLower(email)
	query := "SELECT users.* FROM `users` WHERE `email`=$email"

	// Execute Query
	params := make(map[string]interface{}, 1)
	params["email"] = email

	// Execute Query
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params})
	if err != nil {
		log.Errorf("Cluster.Query() failed with: %v", err)
		return User{}, err
	}
	defer results.Close()

	// Interfaces for handling streaming return values
	var row User

	err = results.One(&row)
	if err != nil {
		log.Errorf("results.One() failed with: %v", err)
		return row, err
	}

	return row, nil
}

// CreateAdminUser creates admin user.
func CreateAdminUser() {
	username := viper.GetString("app.admin_user")
	password := viper.GetString("app.admin_pwd")
	email := viper.GetString("app.admin_email")

	u, _ := GetByUsername(username)
	if u.Username != "" {
		log.Warnf("Admin user %s already exists, skipping", username)
		return
	}

	newUser := User{
		Username: username,
		Email:    email,
	}

	t := time.Now().UTC()
	newUser.MemberSince = &t
	newUser.Admin = true
	newUser.Password = HashAndSalt([]byte(password))
	newUser.Confirmed = true
	newUser.HasAvatar = true
	newUser.Save()

	f := app.SfwAvatarFileDesc
	fi, err := f.Stat()
	if err != nil {
		log.Fatalf("Could not obtain stat, err: %v", err)
	}

	// Upload the sample to the object storage.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = app.MinioClient.PutObject(ctx, app.AvatarSpaceBucket,
		username, app.SfwAvatarFileDesc, fi.Size(),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		log.Fatal("Failed to upload object, err: ", err)
	}

	log.Info("Successfully created admin user")
}

// deleteUser will delete a user
func deleteUser(username string) error {

	// delete user
	_, err := db.UsersCollection.Remove(strings.ToLower(username), &gocb.RemoveOptions{})
	return err
}

// GetUser handle /GET request
func GetUser(c echo.Context) error {

	// get query param `fields` for filtering & sanitize them
	filters := utils.GetQueryParamsFields(c)
	if len(filters) > 0 {
		user := User{}
		allowed := utils.IsFilterAllowed(utils.GetStructFields(user), filters)
		if !allowed {
			return c.JSON(http.StatusBadRequest, "Filters not allowed")
		}
	}

	// get path param
	username := c.Param("username")
	user, err := GetUserByUsernameFields(filters, username)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"verbose_msg": "User not found"})
	}

	// hide sensitive data
	user.Password = ""
	user.Email = ""
	return c.JSON(http.StatusOK, user)
}

// PutUser updates a given user.
func PutUser(c echo.Context) error {

	currentUser := c.Get("user").(*jwt.Token)
	claims := currentUser.Claims.(jwt.MapClaims)
	currentUsername := claims["name"].(string)

	// get path param
	username := c.Param("username")

	if username != currentUsername {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"verbose_msg": "Not allowed to update other users' data"})
	}

	// Read the json body
	b, err := ioutil.ReadAll(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// Verify length
	if len(b) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "You have sent an empty json"})
	}

	// Validate JSON
	l := gojsonschema.NewBytesLoader(b)
	result, err := app.UserUpdateSchema.Validate(l)
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

	// Get user infos.
	u, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exists"})
	}

	// merge it
	err = json.Unmarshal(b, &u)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}
	u.Save()

	// Empty private fields to not be displayed in json
	u.Password = ""
	return c.JSON(http.StatusOK, u)
}

// DeleteUser handle /DELETE request
func DeleteUser(c echo.Context) error {

	currentUser := c.Get("user").(*jwt.Token)
	claims := currentUser.Claims.(jwt.MapClaims)
	currentUsername := claims["name"].(string)

	// get path param
	username := c.Param("username")

	if username != currentUsername {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"verbose_msg": "Not allowed to delete another user account's"})
	}

	// Get user infos.
	_, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exists"})
	}

	// Perform the deletion
	err = deleteUser(username)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"verbose_msg": "User has been deleted successefuly"})
}

// PostUsers adds a new user.
func PostUsers(c echo.Context) error {

	// Read the json body
	b, err := ioutil.ReadAll(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// Verify length
	if len(b) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "You have sent an empty json"})
	}

	// Validate JSON
	l := gojsonschema.NewBytesLoader(b)
	result, err := app.UserSchema.Validate(l)
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

	// Bind it to our User instance.
	newUser := User{}
	err = json.Unmarshal(b, &newUser)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	// check if user already exist in DB.
	u, err := GetByUsername(newUser.Username)
	if err == nil && u.Username != "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username already exists !"})
	}

	// check if email already exists in DB.
	EmailExist, _ := CheckEmailExist(newUser.Email)
	if EmailExist {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Email already exists !"})
	}

	// Update some details
	t := time.Now().UTC()
	newUser.Password = HashAndSalt([]byte(newUser.Password))
	newUser.Name = ""
	newUser.MemberSince = &t
	newUser.Confirmed = false
	newUser.Bio = ""
	newUser.URL = ""
	newUser.Location = ""
	newUser.LastSeen = &t
	newUser.Following = nil
	newUser.Followers = nil
	newUser.Likes = nil
	newUser.Comments = nil
	newUser.Submissions = nil
	newUser.Activities = nil
	newUser.HasAvatar = false
	newUser.Admin = false

	// Creates the new user and save it to DB.
	newUser.Email = strings.ToLower(newUser.Email)
	newUser.Save()

	// Send confirmation email
	token, err := newUser.GenerateEmailConfirmationToken()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": "Internal server error !"})
	}

	// Generate the email confirmation url
	r := c.Request()
	baseURL := c.Scheme() + "://" + r.Host
	link := baseURL + "/v1/auth/confirm/" + "?token=" + token
	go email.Send(newUser.Username, link, newUser.Email, "confirm")

	return c.JSON(http.StatusCreated, map[string]string{
		"verbose_msg": "ok"})
}

// PutUsers bulk updates Users
func PutUsers(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"verbose_msg": "ok"})
}

// DeleteUsers handlers /DELETE
func DeleteUsers(c echo.Context) error {

	// should be processed in the background
	go DeleteAllUsers()
	return c.JSON(http.StatusOK, map[string]string{
		"verbose_msg": "ok"})
}

// GetUsers returns all users.
func GetUsers(c echo.Context) error {

	// get query param `fields` for filtering & sanitize them
	filters := utils.GetQueryParamsFields(c)
	if len(filters) > 0 {
		user := User{}
		allowed := utils.IsFilterAllowed(utils.GetStructFields(user), filters)
		if !allowed {
			return c.JSON(http.StatusBadRequest, "Filters not allowed")
		}
	}

	// get all users
	allUsers, err := GetAllUsers(filters)
	if err != nil {
		return c.JSON(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, allUsers)
}

// GetAvatar returns a user avatar.
func GetAvatar(c echo.Context) error {

	// get path param
	username := c.Param("username")

	// Get user infos.
	usr, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exist"})
	}

	// If the user does not set a custom avatar, we serve a default one.
	if !usr.HasAvatar {
		return c.Blob(http.StatusOK, "image/png", app.AvatarFileBuff)
	}

	// Read it from object storage.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reader, err := app.MinioClient.GetObject(ctx,
		app.AvatarSpaceBucket, username, minio.GetObjectOptions{})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	defer reader.Close()

	_, err = reader.Stat()
	if err != nil {
		return c.JSON(http.StatusNotFound, err.Error())
	}

	return c.Stream(http.StatusOK, "image/png", reader)
}

// UpdateAvatar updates the users' avatar
func UpdateAvatar(c echo.Context) error {

	currentUser := c.Get("user").(*jwt.Token)
	claims := currentUser.Claims.(jwt.MapClaims)
	currentUsername := claims["name"].(string)

	// get path param
	username := c.Param("username")
	if username != currentUsername {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"verbose_msg": "Not allowed to update someone else avatar account's"})
	}

	// Get user infos.
	usr, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exist"})
	}

	// Source
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.JSON(http.StatusCreated, map[string]string{
			"verbose_msg": "Missing file, did you send the file via the form request ?",
		})
	}

	// Check file size
	if fileHeader.Size > app.MaxAvatarFileSize {
		return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{
			"verbose_msg": "File too large. he maximum allowed is 100KB",
			"Filename":    fileHeader.Filename,
		})
	}

	// Open the file
	file, err := fileHeader.Open()
	if err != nil {
		log.Error("Opening a file handle failed, err: ", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": "Unable to open the file",
			"Filename":    fileHeader.Filename,
		})
	}
	defer file.Close()

	// Get the size
	size := fileHeader.Size
	log.Infoln("File size: ", size)

	// Read the content
	fileContents, err := ioutil.ReadAll(file)
	if err != nil {
		log.Error("Opening a reading the file content, err: ", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": "ReadAll failed",
			"Filename":    fileHeader.Filename,
		})
	}

	// Upload the sample to the object storage.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := app.MinioClient.PutObject(ctx, app.AvatarSpaceBucket,
		username, bytes.NewReader(fileContents), size,
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		log.Error("Failed to upload object, err: ", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": "PutObject failed",
			"Description": err.Error(),
			"Filename":    fileHeader.Filename,
		})
	}

	log.Infof("Successfully uploaded bytes: %v", n)

	// Update user
	usr.HasAvatar = true
	usr.Save()

	return c.JSON(http.StatusOK, map[string]string{
		"verbose_msg": "Updated successefuly",
		"Filename":    fileHeader.Filename,
	})
}

// Actions handles the different actions over a user.
func Actions(c echo.Context) error {

	// extract user from token
	u := c.Get("user").(*jwt.Token)
	claims := u.Claims.(jwt.MapClaims)
	username := claims["name"].(string)

	// Get user infos.
	currentUser, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exist"})
	}

	// Read the json body
	b, err := ioutil.ReadAll(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// Verify length
	if len(b) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "You have sent an empty json"})
	}

	// Validate JSON
	l := gojsonschema.NewBytesLoader(b)
	result, err := app.UserActionSchema.Validate(l)
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

	// get the type of action
	var actions map[string]interface{}
	err = json.Unmarshal(b, &actions)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	actionType := actions["type"].(string)

	// get target user
	targetUser, err := GetByUsername(c.Param("username"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Target user does not exist"})
	}

	if currentUser.Username == targetUser.Username {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Not allowed to follow yourself"})
	}

	switch actionType {
	case "follow":
		if !utils.IsStringInSlice(targetUser.Username, currentUser.Following) {
			currentUser.Following = append(currentUser.Following, targetUser.Username)
			currentUser.FollowingCount += 1

			// add new activity
			activity := currentUser.NewActivity("follow", map[string]string{
				"user": targetUser.Username})
			currentUser.Activities = append(currentUser.Activities, activity)
			currentUser.Save()

		}
		if !utils.IsStringInSlice(currentUser.Username, targetUser.Followers) {
			targetUser.Followers = append(targetUser.Followers, currentUser.Username)
			targetUser.FollowersCount += 1
			targetUser.Save()
		}

	case "unfollow":
		if utils.IsStringInSlice(targetUser.Username, currentUser.Following) {
			currentUser.Following = utils.RemoveStringFromSlice(currentUser.Following, targetUser.Username)
			currentUser.FollowingCount -= 1
		}
		if utils.IsStringInSlice(currentUser.Username, targetUser.Followers) {
			targetUser.Followers = utils.RemoveStringFromSlice(targetUser.Followers, currentUser.Username)
			targetUser.FollowersCount -= 1
		}
		currentUser.Save()
		targetUser.Save()
	}

	return c.JSON(http.StatusOK, map[string]string{
		"verbose_msg": "action success",
	})
}

// GetActivitiy represents the feed displayed in the landing page for logged in users.
func GetActivitiy(c echo.Context) error {

	// get path param
	username := c.Param("username")

	// Get user infos.
	_, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exists"})
	}

	// Get all activities from all users whom I am following.
	params := make(map[string]interface{}, 1)
	params["user"] = username
	query :=
		"SELECT t1.*, f.tags, array_count(array_flatten(array i.infected " +
			"for i in OBJECT_VALUES(f.multiav.last_scan) when i.infected=true end, 1)) as av_count " +
			"FROM ( " +
			"SELECT u.`username`, `activity`.* " +
			"FROM `users` u " +
			"UNNEST `activities` AS activity " +
			"WHERE u.`username` IN " +
			"(SELECT RAW u1.`following` FROM users u1 " +
			"WHERE u1.username= $user)[0] " +
			") t1  " +
			"LEFT JOIN `files` f ON KEYS t1.content.sha256 " +
			"WHERE f.status == 2 " +
			"UNION " +
			"SELECT u.`username`,  `activity`.* " +
			"FROM `users` u " +
			"UNNEST `activities` AS activity " +
			"WHERE activity.`type` == 'follow' AND activity.`content`.`user` == $user"

	// Execute Query
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params, Adhoc: true})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": err.Error(),
		})
	}
	defer results.Close()

	// Interfaces for handling streaming return values
	var activities []interface{}
	var row interface{}

	// Stream the values returned from the query into a typed array of structs
	for results.Next() {
		err := results.Row(&row)
		if err != nil {
			log.Errorf("results.Row() failed with: %v", err)
		}
		activities = append(activities, row)
	}
	if len(activities) == 0 {
		return c.JSON(http.StatusOK, []map[string]string{})
	}
	return c.JSON(http.StatusOK, activities)
}

// GetActivities represents the feed displayed in the landing page for anonymous users.
func GetActivities(c echo.Context) error {

	// Get all activities from all users.
	params := make(map[string]interface{}, 1)
	params["user"] = viper.GetString("app.admin_user")
	query := "SELECT t1.*, f.tags, " +
		"array_count(array_flatten(array i.infected for i " +
		"in OBJECT_VALUES(f.multiav.last_scan) when " +
		"i.infected=true end, 1)) as av_count " +
		"FROM (SELECT u.`username`, `activity`.* " +
		"FROM `users` u " +
		"UNNEST `activities` AS activity " +
		"WHERE u.`username` != $user AND u.`activities` IS NOT NULL " +
		"ORDER BY activity.timestamp DESC LIMIT 30 " +
		") t1 LEFT JOIN `files` f ON KEYS t1.content.sha256 " +
		"WHERE f.status == 2"

	// Execute Query
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": err.Error(),
		})
	}
	defer results.Close()

	// Interfaces for handling streaming return values
	var activities []interface{}
	var row interface{}

	// Stream the values returned from the query into a typed array of structs
	for results.Next() {
		err := results.Row(&row)
		if err != nil {
			log.Errorf("results.Row() failed with: %v", err)
		}
		activities = append(activities, row)
	}

	if len(activities) == 0 {
		return c.JSON(http.StatusOK, []map[string]string{})
	}
	return c.JSON(http.StatusOK, activities)
}

// GetLikes returns list of likes samples with metadata.
func GetLikes(c echo.Context) error {

	// get path param
	username := c.Param("username")

	// Get user infos.
	usr, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exist"})
	}

	// Get all activities from all users.
	params := make(map[string]interface{}, 1)
	params["user"] = usr.Username
	query := `
		SELECT sha256, submissions[0].filename, 
		 ml.pe.predicted_class as class, tags,
		 CONCAT(
			TOSTRING(
				 ARRAY_COUNT(array_flatten(array i.infected 
		    	for i in OBJECT_VALUES(f.multiav.last_scan) 
		 		when i.infected=true end, 1))
			), "/", TOSTRING(OBJECT_LENGTH(f.multiav.last_scan))
		) as multiav
  		FROM files f
		USE KEYS [(SELECT raw likes FROM users u USE KEYS $user )[0]]
		`

	// Execute Query
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": err.Error(),
		})
	}
	defer results.Close()

	// Interfaces for handling streaming return values
	var likes []interface{}
	var row interface{}

	// Stream the values returned from the query into a typed array of structs
	for results.Next() {
		err := results.Row(&row)
		if err != nil {
			log.Errorf("results.Row() failed with: %v", err)
		}
		likes = append(likes, row)
	}

	if len(likes) == 0 {
		return c.JSON(http.StatusOK, []map[string]string{})
	}
	return c.JSON(http.StatusOK, likes)

}

// GetSubmissions returns list of submissions with metadata.
func GetSubmissions(c echo.Context) error {

	// get path param
	username := c.Param("username")

	// Get user infos.
	usr, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exist"})
	}

	// Get all activities from all users.
	params := make(map[string]interface{}, 1)
	params["user"] = usr.Username
	query := `
		SELECT s.*, f.submissions[0].filename,
		f.ml.pe.predicted_class as class, f.tags,
		ARRAY_BINARY_SEARCH(ARRAY_SORT(u.likes), s.sha256) > 0 as liked,
		CONCAT(
			TOSTRING(
				ARRAY_COUNT(array_flatten(array i.infected 
		   		for i in OBJECT_VALUES(f.multiav.last_scan) 
				when i.infected=true end, 1))
		), "/", TOSTRING(OBJECT_LENGTH(f.multiav.last_scan))) as multiav 
		FROM users u 
		USE KEYS $user 
		UNNEST u.submissions AS s 
		JOIN files f ON KEYS s.sha256;
		`

	// Execute Query
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": err.Error(),
		})
	}
	defer results.Close()

	// Interfaces for handling streaming return values
	var likes []interface{}
	var row interface{}

	// Stream the values returned from the query into a typed array of structs
	for results.Next() {
		err := results.Row(&row)
		if err != nil {
			log.Errorf("results.Row() failed with: %v", err)
		}
		likes = append(likes, row)
	}

	if len(likes) == 0 {
		return c.JSON(http.StatusOK, []map[string]string{})
	}
	return c.JSON(http.StatusOK, likes)
}


// GetFollowing returns list of followed users by this user with metadata.
func GetFollowing(c echo.Context) error {

	// get path param
	username := c.Param("username")

	// Get user infos.
	usr, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exist"})
	}

	// Get all activities from all users.
	params := make(map[string]interface{}, 1)
	params["user"] = usr.Username
	query := `
		SELECT u.member_since, u.username 
		FROM users u 
		USE KEYS` + " [(SELECT raw nu.`following` " + 
		`FROM users nu USE KEYS $user )[0]]
		`

	// Execute Query
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": err.Error(),
		})
	}
	defer results.Close()

	// Interfaces for handling streaming return values
	var likes []interface{}
	var row interface{}

	// Stream the values returned from the query into a typed array of structs
	for results.Next() {
		err := results.Row(&row)
		if err != nil {
			log.Errorf("results.Row() failed with: %v", err)
		}
		likes = append(likes, row)
	}

	if len(likes) == 0 {
		return c.JSON(http.StatusOK, []map[string]string{})
	}
	return c.JSON(http.StatusOK, likes)
}

// GetFollowers returns list of users following this user with metadata.
func GetFollowers(c echo.Context) error {

	// get path param
	username := c.Param("username")

	// Get user infos.
	usr, err := GetByUsername(username)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"verbose_msg": "Username does not exist"})
	}

	// Get all activities from all users.
	params := make(map[string]interface{}, 1)
	params["user"] = usr.Username
	query := `
		SELECT u.member_since, u.username,` +
		"ARRAY_BINARY_SEARCH(ARRAY_SORT(u.`following`), $user) > 0 as followed " +
		`FROM users u 
		USE KEYS` + " [(SELECT raw nu.`followers` " + 
		`FROM users nu USE KEYS $user )[0]];
		`

	// Execute Query
	results, err := db.Cluster.Query(query,
		&gocb.QueryOptions{NamedParameters: params})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"verbose_msg": err.Error(),
		})
	}
	defer results.Close()

	// Interfaces for handling streaming return values
	var likes []interface{}
	var row interface{}

	// Stream the values returned from the query into a typed array of structs
	for results.Next() {
		err := results.Row(&row)
		if err != nil {
			log.Errorf("results.Row() failed with: %v", err)
		}
		likes = append(likes, row)
	}

	if len(likes) == 0 {
		return c.JSON(http.StatusOK, []map[string]string{})
	}
	return c.JSON(http.StatusOK, likes)
}