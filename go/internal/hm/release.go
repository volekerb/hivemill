package hm

import (
	"path"
	"regexp"
	"strings"
)

// The release hard limit (G-075 criterion 3): agents may upload TestFlight builds, but a public App Store submit or
// release needs the owner's answer "release build N" on record. `hivemill guard` enforces it on every Bash call.

var (
	// ascReleasePaths are the App Store Connect API resources that submit or release a version.
	ascReleasePaths = []string{"reviewSubmissions", "appStoreVersionSubmissions", "appStoreVersionReleaseRequests"}
	// releaseBuildRe finds the build numbers a command names: build_number:N, --build_number N, --build-number N, build N.
	releaseBuildRe = regexp.MustCompile(`(?:\bbuild_number[:= ]|--build-number[= ]|\bbuild )[ \t]*([0-9]+(?:\.[0-9]+)*)`)
	shellSplitRe   = regexp.MustCompile("[;&|\n()`]+")
	shellText      = strings.NewReplacer("\\\n", " ", `"`, "", `'`, "", `\`, "", ">&", " ", "&>", " ", ">", " ", "<", " ")
)

// ReleaseAction classifies a shell command. submit is true for a public App Store submit or release (a TestFlight
// upload is not one). build is the build number the command names, empty when it names none or two different ones.
func ReleaseAction(command string) (submit bool, build string) {
	text := shellText.Replace(command)
	for _, p := range ascReleasePaths {
		submit = submit || strings.Contains(text, p)
	}
	for _, seg := range shellSplitRe.Split(text, -1) {
		submit = submit || fastlaneSubmit(strings.Fields(seg))
	}
	for _, m := range releaseBuildRe.FindAllStringSubmatch(text, -1) {
		if build != "" && build != m[1] {
			return submit, ""
		}
		build = m[1]
	}
	if strings.Contains(build, ".") {
		build = ""
	}
	return submit, build
}

// fastlaneSubmit is true when the words of one simple command run a fastlane release or submit.
func fastlaneSubmit(words []string) bool {
	for i, w := range words {
		switch path.Base(w) {
		case "fastlane":
			if lane := fastlaneLane(words[i+1:]); lane == "release" || lane == "submit_review" {
				return true
			}
		case "deliver", "upload_to_app_store":
			if i+1 < len(words) && words[i+1] == "submit_build" {
				return true
			}
		default:
			continue
		}
		return submitForReview(words)
	}
	return false
}

// fastlaneLane is the lane or action fastlane is asked to run: the first word that is not an option, a platform or `run`.
func fastlaneLane(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--env":
			i++
		case strings.HasPrefix(a, "-"), a == "ios", a == "mac", a == "run":
		case a == "deliver" || a == "upload_to_app_store" || a == "appstore":
			if i+1 < len(args) && args[i+1] == "submit_build" {
				return "submit_review"
			}
			return a
		default:
			return a
		}
	}
	return ""
}

// submitForReview is true when the option submit_for_review is on: as a flag, key:value or an environment variable.
func submitForReview(words []string) bool {
	for i, w := range words {
		w = strings.ToLower(w)
		at := strings.Index(w, "submit_for_review")
		if at < 0 {
			continue
		}
		value := strings.TrimLeft(w[at+len("submit_for_review"):], ":=")
		if value == "" && i+1 < len(words) {
			value = strings.ToLower(words[i+1])
		}
		if value != "false" && value != "no" {
			return true
		}
	}
	return false
}

// ReleaseAllowed is true only when build is a number and the owner answered a question with the literal words
// "release build N" for exactly that N.
func ReleaseAllowed(asks []Ask, build string) bool {
	if build == "" || strings.Trim(build, "0123456789") != "" {
		return false
	}
	// N ends at the text end or at punctuation: 12 does not allow 123, 12.1, 12-1 or 12a.
	words := regexp.MustCompile(`(?:^|\W)release build ` + build + `(?:$|[.\-](?:$|\W)|[^\w.\-])`)
	for _, a := range asks {
		if a.Status == "answered" && a.AnsweredBy == "owner" && words.MatchString(a.Answer) {
			return true
		}
	}
	return false
}
