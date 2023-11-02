package mailer

import (
	"bytes"
	"embed"
	"html/template"
	"time"

	"github.com/go-mail/mail/v2"
)

// Email templates will be built into our binary - no need to deploy templates
// separately to prod server.

// Declare type to hold our email templates. Uses embedded path directive comment

// ↓↓↓

//go:embed "templates"
var templatFS embed.FS

// Dialer instance used to connect to a SMTP server and sender info for emails.
type Mailer struct {
	dialer *mail.Dialer
	sender string
}

func New(host string, port int, username, password, sender string) Mailer {
	dialer := mail.NewDialer(host, port, username, password)
	dialer.Timeout = 5 * time.Second

	return Mailer{
		dialer: dialer,
		sender: sender,
	}
}

// Takes recipient email address, name of file containing the templates, and any
// dynamic data for the templates as an any param.
func (m Mailer) Send(recipient, templateFile string, data any) error {
	// Parse the required template file from embedded file system.
	tmpl, err := template.New("email").ParseFS(templatFS, "templates/"+templateFile)
	if err != nil {
		return err
	}

	// Execute the named template "subject", passing in dynamic data and storing
	// the result in a bytes.buffer var.
	subject := new(bytes.Buffer)
	err = tmpl.ExecuteTemplate(subject, "subject", data)
	if err != nil {
		return err
	}

	// Same pattern for "plainBody"
	plainBody := new(bytes.Buffer)
	err = tmpl.ExecuteTemplate(plainBody, "plainBody", data)
	if err != nil {
		return err
	}

	// And same for "htmlBody" template
	htmlBody := new(bytes.Buffer)
	err = tmpl.ExecuteTemplate(htmlBody, "htmlBody", data)
	if err != nil {
		return err
	}

	// Init a new mail.Message instance. Order matters.
	msg := mail.NewMessage()
	msg.SetHeader("To", recipient)
	msg.SetHeader("From", m.sender)
	msg.SetHeader("Subject", subject.String())
	msg.SetBody("text/plain", plainBody.String())
	msg.AddAlternative("text/html", htmlBody.String())

	// Retry twice, with 500ms sleeps between.
	for i := 1; i <= 3; i++ {
		err = m.dialer.DialAndSend(msg)
		// nil first to make it visually jarring.
		if nil == err {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return err
}
