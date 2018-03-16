package main


import (
    "bytes"
    "fmt"
    "log"
    "net/smtp"
    "os"
    "reflect"
    "regexp"
    "strings"

    "github.com/andygrunwald/go-gerrit"
    "github.com/boltdb/bolt"
    "github.com/go-ini/ini"
)


type WatchedGerrit struct {
    Url string
    Projects []string
    Rexps []string
    Mail string
}


var DEFAULT_CONFIG = "~/.config/scrutiny.conf"
var GERRITS = []WatchedGerrit{
    WatchedGerrit{
        Mail: "rhos-mm@redhat.com",
        Url: "https://review.openstack.org/",
        Projects: []string{
            "openstack/tripleo-common",
            "openstack/tripleo-heat-templates",
            "openstack/kolla",
        },
        Rexps: []string{
            "[Aa]odh",
            "[Cc]eilometer",
            "[Cc]ollectd",
            "[Ff]luentd",
            "[Gg]nocchi",
            "[Rr]syslog",
            "[Ss]ensu",
            "[Hh]ealth",
            "[Mm]etric",
            "[Ll]og ",
            "[Ll]ogg",
            "[Mm]ongodb",
            "[Mm]onitor",
            "[Pp]anko",
            "[Rr]edis",
        },
    },
}
var MAIL_SMTP = "smtp.corp.redhat.com"
var MAIL_PORT = 25
var MAIL_SENDER = "mmagr@redhat.com"
var MAIL_SUBJECT = "[gerrit] Changes required attention"
var MAIL_HEADER = `
Following gerrit changes potentialy require your attention. Please check:
`


/*
* Logs given message with error message. Exits if level is "error".
*/
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


func sendMail(server string, port int, sender string, recipient string, body string) error {
    c, err := smtp.Dial(fmt.Sprintf("%s:%d", server, port))
    if err != nil {
        return err
    }
    defer c.Close()
    // Set the sender and recipient.
    c.Mail(sender)
    c.Rcpt(recipient)

    wc, err := c.Data()
    if err != nil {
        return err
    }
    defer wc.Close()

    buff := bytes.NewBufferString(body)
    if _, err = buff.WriteTo(wc); err != nil {
        return err
    }
    return nil
}


/*
* Opens DB connections and creates bucket for each gerrit instance
* if it does not exist
*/
func setupDB(cfg ini.File) (*bolt.DB, error) {
    db, err := bolt.Open(cfg.Section("").Key("db").String(), 0600, nil)
    if err != nil {
        return nil, fmt.Errorf("Could not open db: %v", err)
    }
    err = db.Update(func(tx *bolt.Tx) error {
        for _, instance := range GERRITS {
            _, err := tx.CreateBucketIfNotExists([]byte(instance.Url))
            if err != nil {
                return fmt.Errorf("Could not create root bucket: %v", err)
            }
        }
        return nil
    })
    if err != nil {
        return nil, fmt.Errorf("could not set up buckets, %v", err)
    }
    return db, nil
}


/*
* Retuns true if given change was already reported, otherwise returns false
*/
func shouldInclude(db *bolt.DB, instance WatchedGerrit, change gerrit.ChangeInfo) bool {
    exists := false

    err := db.View(func(tx *bolt.Tx) error {
        val := tx.Bucket([]byte(instance.Url)).Get([]byte(fmt.Sprintf("%d", change.Number)))
        if val != nil {
            report("info", fmt.Errorf("%d", change.Number), "Already reported change")
            exists = true
        }
        return nil
    })
    if err != nil {
        report("error", err, "Failed to browse DB.")
    }

    if !exists {
        report("info", fmt.Errorf("%d", change.Number), "New change")
        err := db.Update(func(tx *bolt.Tx) error {
            return tx.Bucket([]byte(instance.Url)).Put([]byte(fmt.Sprintf("%d", change.Number)), []byte("1"))
        })
        if err != nil {
            report("error", err, "Failed to update DB.")
        }
    }

    return !exists
}


/*
* Cleans db records of changes which are not reported by gerrit any more
*/
func cleanDb(db *bolt.DB, instance WatchedGerrit, open []gerrit.ChangeInfo) {
    openChanges := map[string]bool{};

    for _, change := range open {
        openChanges[fmt.Sprintf("%d", change.Number)] = true
    }

    db.Update(func(tx *bolt.Tx) error {
        bkt := tx.Bucket([]byte(instance.Url))
        c := b.Cursor()
        for key, _ := c.First(); key != nil; key, _ = c.Next() {
            if _, exists := openChanges[string(key)]; !exists {
                bkt.Delete(key)
            }
        }
        return nil
    })
}


func loadConfig() ini.File {
    config := ""
    if value, ok := os.LookupEnv(key); ok {
        config = value
    } else {
        config = DEFAULT_CONFIG
    }
    return ini.Load(config)
}


/*
* Loads data from config to WatchedGerrit
*/
func initInstances(cfg ini.File) []WatchedGerrit {
    output := []WatchedGerrit{}
    for gerrit := strings.Split(cfg.Section("").Key("gerrits").String(), ",") {
        instance := WatchedGerrit{}
        section, err := cfg.GetSection(fmt.Sprintf("gerrit:%s"))
        if err != nil {
            report("error", err, "Failed to load section in config")
            continue
        }

        singleValues := [2]string{"mail", "url"}
        for _, key := range singleValues {
            key, err := section.GetKey(key)
            if err == nil {
                val := reflect.ValueOf(instance).Elem().FieldByName(key)
                if val.IsValid() {
                    val.SetString(key.String())
                }
            }
        }

        multiValues := [2]string{"projects", "regexps"}
        for _, key := range multiValues {
            key, err := section.GetKey(key)
            if err == nil {
                val := reflect.ValueOf(instance).Elem().FieldByName(key)
                if val.IsValid() {
                    val.Set(strings.Split(key.String(), ","))
                }
            }
        }

        append(output, instance)
    }
}


func main() {
    cfg := loadConfig()
    allGerrits := initInstances(cfg)

    db, err := setupDB(cfg)
    if err != nil {
        report("error", err, "Failed to initialize DB.")
    }
    defer db.Close()

    for _, instance := range allGerrits {
        open := []gerrit.ChangeInfo{}
        found := []gerrit.ChangeInfo{}
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
                    if len(r.FindString(change.Revisions[change.CurrentRevision].Commit.Message)) > 0 {
                        open = append(open, change)
                        if shouldInclude(db, instance, change) {
                            found = append(found, change)
                        }
                        continue
                    }
                }
            }
        }

        if len(found) > 0 {
            body := fmt.Sprintf("From: %s\r\n", MAIL_SENDER)
            body += fmt.Sprintf("To: %s\r\n", instance.Mail)
            body += fmt.Sprintf("Subject: %s\r\n", MAIL_SUBJECT)
            body += "\r\n" + MAIL_HEADER
            for _, change := range found {
                body = body + fmt.Sprintf("[%s] %s: %s%d\r\n\r\n", change.Project, change.Subject, instance.Url, change.Number)
            }
            err := sendMail(MAIL_SMTP, MAIL_PORT, MAIL_SENDER, instance.Mail, body)
            if err != nil {
                report("error", err, fmt.Sprintf("Unable to send report for gerrit instance %s. Report message:\n%s", instance.Url, body))
            }
        }

        cleanDb(db, instance, open)
    }
}
