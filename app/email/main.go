// Copyright 2020 Saferwall. All rights reserved.
// Use of this source code is governed by Apache v2 license
// license that can be found in the LICENSE file.

package email

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/go-gomail/gomail"
	"github.com/matcornic/hermes/v2"
	"github.com/saferwall/saferwall/web/app"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"net/mail"
	"os"
)

type template interface {
	Email(string, string) hermes.Email
	Name() string
}

// Send sends an email.
func Send(username, link, recipient, templateToUse string) {

	// Get our Hermes instance
	h := app.Hermes

	// Use the default theme
	h.Theme = new(hermes.Default)

	options := sendOptions{
		To: recipient,
	}

	var t template

	if templateToUse == "confirm" {
		t = new(confirm)
		options.Subject = "Saferwall - Confirm Account"

	}
	if templateToUse == "reset" {
		t = new(reset)
		options.Subject = "Saferwall - Reset Password"
	}

	// Generate emails
	generateEmails(h, t.Email(username, link), t.Name())

	log.Printf("Sending email '%s'...", options.Subject)
	path := fmt.Sprintf("%v/%v.%v.html", h.Theme.Name(), h.Theme.Name(), t.Name())
	htmlBytes, err := ioutil.ReadFile(path)
	if err != nil {
		panic(err)
	}
	txtBytes, err := ioutil.ReadFile(fmt.Sprintf("%v/%v.%v.txt", h.Theme.Name(), h.Theme.Name(), t.Name()))
	if err != nil {
		panic(err)
	}
	err = send(options, string(htmlBytes), string(txtBytes))
	if err != nil {
		panic(err)
	}
	log.Println("email sent success")

}

func generateEmails(h hermes.Hermes, email hermes.Email, example string) {
	// Generate the HTML template and save it
	res, err := h.GenerateHTML(email)
	if err != nil {
		panic(err)
	}
	err = os.MkdirAll(h.Theme.Name(), 0744)
	if err != nil {
		panic(err)
	}
	err = ioutil.WriteFile(fmt.Sprintf("%v/%v.%v.html", h.Theme.Name(), h.Theme.Name(), example), []byte(res), 0644)
	if err != nil {
		panic(err)
	}

	// Generate the plaintext template and save it
	res, err = h.GeneratePlainText(email)
	if err != nil {
		panic(err)
	}
	err = ioutil.WriteFile(fmt.Sprintf("%v/%v.%v.txt", h.Theme.Name(), h.Theme.Name(), example), []byte(res), 0644)
	if err != nil {
		panic(err)
	}
}

// sendOptions are options for sending an email
type sendOptions struct {
	To      string
	Subject string
}

// send sends the email
func send(options sendOptions, htmlBody string, txtBody string) error {

	smtpConfig := app.SMTPConfig

	if smtpConfig.Server == "" {
		return errors.New("SMTP server config is empty")
	}
	if smtpConfig.Port == 0 {
		return errors.New("SMTP port config is empty")
	}

	if smtpConfig.SMTPUser == "" {
		return errors.New("SMTP user is empty")
	}

	if smtpConfig.SenderIdentity == "" {
		return errors.New("SMTP sender identity is empty")
	}

	if smtpConfig.SenderEmail == "" {
		return errors.New("SMTP sender email is empty")
	}

	if options.To == "" {
		return errors.New("no receiver emails configured")
	}

	from := mail.Address{
		Name:    smtpConfig.SenderIdentity,
		Address: smtpConfig.SenderEmail,
	}

	m := gomail.NewMessage()
	m.SetHeader("From", from.String())
	m.SetHeader("To", options.To)
	m.SetHeader("Subject", options.Subject)

	m.SetBody("text/plain", txtBody)
	m.AddAlternative("text/html", htmlBody)

	d := gomail.NewDialer(smtpConfig.Server, smtpConfig.Port, smtpConfig.SMTPUser, smtpConfig.SMTPPassword)

	// skip verification to avoid: panic: x509: certificate signed by unknown authority
	d.TLSConfig = &tls.Config{InsecureSkipVerify: true}

	return d.DialAndSend(m)
}
