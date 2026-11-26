package hm

import "testing"

func ownerAnswer(text string) []Ask {
	return []Ask{{Status: "answered", AnsweredBy: "owner", Answer: text}}
}

func TestReleaseSubmitRefusedWithoutOwnerAnswer(t *testing.T) {
	for _, c := range []string{
		"fastlane deliver --submit_for_review --build_number 7",
		"fastlane deliver --submit_for_review true --build-number 7",
		"bundle exec fastlane deliver submit_for_review:true build_number:7",
		"fastlane run upload_to_app_store submit_for_review:true build_number:7",
		"DELIVER_SUBMIT_FOR_REVIEW=true fastlane deliver --build_number 7",
		"fastlane deliver submit_build --build_number 7",
		"cd app && fastlane release build_number:7",
		"bundle exec fastlane ios release build_number:7 2>&1 | tee log",
		"fastlane --env prod submit_review build_number:7",
		`sh -c "fastlane release build_number:7"`,
		`curl -X POST https://api.appstoreconnect.apple.com/v1/reviewSubmissions -d 'build 7'`,
		"curl https://api.appstoreconnect.apple.com/v1/appStoreVersionSubmissions # build 7",
		"curl https://api.appstoreconnect.apple.com/v1/appStoreVersionReleaseRequests # build 7",
	} {
		submit, build := ReleaseAction(c)
		if !submit || build != "7" {
			t.Errorf("%q: submit %v build %q, want a submit of build 7", c, submit, build)
		}
		if ReleaseAllowed(nil, build) {
			t.Errorf("%q: allowed with no answer", c)
		}
	}
	for _, c := range []string{"fastlane release", "echo build 7; fastlane release build_number:9", "fastlane release build 1.2"} {
		if submit, build := ReleaseAction(c); !submit || build != "" {
			t.Errorf("%q: submit %v build %q, want a submit with no build", c, submit, build)
		}
	}
	if ReleaseAllowed(ownerAnswer("release build "), "") {
		t.Error("allowed an empty build")
	}
}

func TestReleaseSubmitAllowedWithLiteralAnswer(t *testing.T) {
	for _, text := range []string{"release build 7", "yes, release build 7.", "ok: release build 7 today"} {
		if !ReleaseAllowed(ownerAnswer(text), "7") {
			t.Errorf("%q does not allow build 7", text)
		}
	}
	for _, text := range []string{"yes", "release 7", "prerelease build 7", "build 7"} {
		if ReleaseAllowed(ownerAnswer(text), "7") {
			t.Errorf("%q allows build 7", text)
		}
	}
}

func TestReleaseOtherBuildRefused(t *testing.T) {
	asks := ownerAnswer("release build 12")
	for _, build := range []string{"123", "1", "2"} {
		if ReleaseAllowed(asks, build) {
			t.Errorf("an answer for 12 allows %s", build)
		}
	}
	for _, text := range []string{"release build 123", "release build 12.1", "release build 12-1", "release build 12a"} {
		if ReleaseAllowed(ownerAnswer(text), "12") {
			t.Errorf("%q allows build 12", text)
		}
	}
	for _, a := range []Ask{
		{Status: "answered", AnsweredBy: "planner", Answer: "release build 12"},
		{Status: "withdrawn", AnsweredBy: "owner", Answer: "release build 12"},
		{Status: "open", AnsweredBy: "owner", Answer: "release build 12"},
	} {
		if ReleaseAllowed([]Ask{a}, "12") {
			t.Errorf("%+v allows build 12", a)
		}
	}
}

func TestTestFlightUploadAllowed(t *testing.T) {
	for _, c := range []string{
		"fastlane pilot upload --build_number 7",
		`fastlane pilot upload --changelog "release"`,
		"fastlane run upload_to_testflight",
		"fastlane upload_to_testflight",
		"fastlane ios beta",
		"xcrun altool --upload-app -f build/release.ipa -t ios",
		"xcrun notarytool submit app.zip --wait",
		"fastlane deliver --submit_for_review false",
		"fastlane deliver submit_for_review:false",
		"git push origin release",
		"grep submit_for_review fastlane/Fastfile",
	} {
		if submit, _ := ReleaseAction(c); submit {
			t.Errorf("%q is classified as an App Store submit", c)
		}
	}
}
