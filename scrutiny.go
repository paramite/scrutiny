package main


import (
    "bytes"
    "fmt"
    "log"
    "net/smtp"
    "regexp"
    "strings"

    "github.com/andygrunwald/go-gerrit"
)


type WatchedGerrit struct {
    Url string
    Projects []string
    Rexps []string
    Mail string
}


var GERRITS = []WatchedGerrit{
    WatchedGerrit{
        Mail: "rhos-opstools-dept-list@redhat.com",
        Url: "https://review.openstack.org/",
        Projects: []string{
            "openstack/tripleo-common",
            "openstack/tripleo-heat-templates",
        },
        Rexps: []string{
            "[Cc]ollectd",
            "[Ff]luentd",
            "[Gg]nocchi",
            "[Rr]syslog",
            "[Ss]ensu",
            "[Hh]ealth",
            "[Mm]etric",
            "[Ll]og ",
            "[Ll]ogg",
        },
    },
}
var MAIL_SMTP = "smtp.corp.redhat.com"
var MAIL_SENDER = "mmagr@redhat.com"
var MAIL_SUBJECT = "[gerrit] Changes required attention"
var MAIL_HEADER = `
Scrutiny found following gerrit changes which potentialy require your attention. Please check:

`

func report(level string, err error, msg string) {
    if err != nil {
        var handle func(string, ...interface{})
        switch level {
            case "error":
                handle = log.Fatalf
            default:
                handle = log.Printf
        }
        handle("[%s] %s: %s", strings.ToUpper(level), msg, err)
	}
}


func main() {
    var found []gerrit.ChangeInfo
    for _, instance := range GERRITS {
        client, err := gerrit.NewClient(instance.Url, nil)
        if err != nil {
            report("error", err, "Unable to create gerrit client.")
        }

        for _, project := range instance.Projects {
            opt := &gerrit.QueryChangeOptions{}
            opt.Query = []string{
                fmt.Sprintf("project:%s+status:open", project),
            }
            opt.AdditionalFields = []string{"CURRENT_REVISION", "CURRENT_COMMIT"}
            changes, _, err := client.Changes.QueryChanges(opt)
            if err != nil {
                report("error", err, "Unable to query changes.")
            }

            for _, rexp := range instance.Rexps {
                r, _ := regexp.Compile(rexp)

                for _, change := range *changes {
                    if len(r.FindString(change.Subject)) > 0 {
                        found = append(found, change)
                        continue
                    }
                    if len(r.FindString(change.Revisions[change.CurrentRevision].Commit.Message)) > 0 {
                        found = append(found, change)
                        continue
                    }
                }
            }
        }

        msg := MAIL_HEADER
        for _, change := range found {
            msg = msg + fmt.Sprintf("[%s] %s: %s%d\n", change.Project, change.Subject, instance.Url, change.Number)
        }
        fmt.Printf(msg)

        c, err := smtp.Dial(fmt.Sprintf("%s:25", MAIL_SMTP))
    	if err != nil {
            report("error", err, "Unable to connect to SMTP server.")
    	}
    	defer c.Close()
    	// Set the sender and recipient.
    	c.Mail(MAIL_SENDER)
    	c.Rcpt(instance.Mail)
    	// Send the email body.
        body := fmt.Sprintf("From: %s\r\n", MAIL_SENDER)
		body += fmt.Sprintf("To: %s\r\n", instance.Mail)
	    body += fmt.Sprintf("Subject: %s\r\n", MAIL_SUBJECT)
        body += "\r\n" + msg

    	wc, err := c.Data()
    	if err != nil {
    		report("error", err, "Unable to send DATA command.")
    	}
    	defer wc.Close()

    	buff := bytes.NewBufferString(body)
    	if _, err = buff.WriteTo(wc); err != nil {
    		report("error", err, "Unable to send body data.")
    	}
    }
}
