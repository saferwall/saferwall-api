// Copyright 2021 Saferwall. All rights reserved.
// Use of this source code is governed by Apache v2 license
// license that can be found in the LICENSE file.

package mailer

import (
	"crypto/tls"
	"time"

	mail "github.com/xhit/go-simple-mail/v2"
)

type SMTPServer struct {
	server mail.SMTPServer
}

type SMTPClient struct {
	client *mail.SMTPClient
}

// New creates a new SMTP client using the default configuration.
func New(host string, port int, username, password string,
	encryption int) *mail.SMTPServer {
	server := mail.NewSMTPClient()

	// SMTP Server
	server.Host = host
	server.Port = port
	server.Username = username
	server.Password = password
	server.Encryption = mail.Encryption(encryption)

	// Since v2.3.0 you can specified authentication type:
	// - PLAIN (default)
	// - LOGIN
	// - CRAM-MD5
	server.Authentication = mail.AuthPlain

	// Variable to keep alive connection
	server.KeepAlive = false

	// Timeout for connect to SMTP Server
	server.ConnectTimeout = 10 * time.Second

	// Timeout for send the data and wait respond
	server.SendTimeout = 10 * time.Second

	// Set TLSConfig to provide custom TLS configuration. For example,
	// to skip TLS verification (useful for testing):
	server.TLSConfig = &tls.Config{InsecureSkipVerify: true}

	return server
}

func (s SMTPServer) Connect() (*mail.SMTPClient, error) {
	smtpClient, err := s.server.Connect()
	if err != nil {
		return nil, err
	}
	return smtpClient, nil
}

func (c SMTPClient) Send(htmlBody, subject, from string) error {

	// New email simple html with inline and CC
	email := mail.NewMSG()
	email.SetFrom(from).SetSubject(subject)
	email.SetBody(mail.TextHTML, htmlBody)

	// always check error after send
	if email.Error != nil {
		return email.Error
	}

	// Call Send and pass the client
	return email.Send(c.client)
}