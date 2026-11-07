package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"fyne.io/fyne/v2/dialog"

	"github.com/uidbz/fyne-addons/kdefilepicker"
)

// localProjectWarning reports whether the embedded core is anchored at the
// user's home directory (the desktop launcher's cwd) or at /. That makes the
// permission jail effectively unbounded — grants saved there cover every
// file the user can reach — and project-scoped tools (gortex, project skills,
// saved grants) do not know which repository is meant.
func localProjectWarning() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	wd = filepath.Clean(wd)
	if wd == string(filepath.Separator) {
		return wd, true
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && wd == filepath.Clean(home) {
		return wd, true
	}
	return wd, false
}

// showHomeDirNotice adds a one-time card explaining the problem and offering
// a project picker. Remote backends own their work dir, so this is local-only.
func (s *shell) showHomeDirNotice() {
	wd, bad := localProjectWarning()
	if !bad || s.b.remote() {
		return
	}
	s.appendBlock(s.actionBlock("Working in your home directory",
		fmt.Sprintf("This instance's project is %s. Permission grants saved here apply "+
			"to everything you can reach, and project-scoped tools (gortex, project "+
			"skills) have no repository to work with. Open the project folder instead "+
			"— File ▸ Open project… or start anneal with -C <dir>.", wd),
		"Choose project…", s.chooseProject))
}

// showFolderPicker presents the KDE-style folder picker (fyne-addons
// kdefilepicker) as an overlay on the main window and hands the chosen
// directory to onPick. Its palette follows the Anneal theme (see
// applyFilePickerPalette), so it reads as part of the shell rather than a
// foreign dialog.
func (s *shell) showFolderPicker(onPick func(string)) {
	d := kdefilepicker.NewDirectoryDialog(s.b.cwd())
	d.SetOpenLabel("Choose")
	d.SetOnAccept(onPick)
	d.ShowOverlay(s.win)
}

// chooseProject restarts the app in another directory: the instance's project
// is fixed for its lifetime (permissions, skills, session cwd and the MCP
// server's view are all anchored to it).
func (s *shell) chooseProject() {
	s.showFolderPicker(func(dir string) {
		dialog.ShowConfirm("Open project",
			"Restart Anneal in "+dir+"? This window will close.", func(ok bool) {
				if !ok {
					return
				}
				exe, err := os.Executable()
				if err != nil {
					s.notice(noticeWarn, "Restart: "+err.Error())
					return
				}
				cmd := exec.Command(exe, "-C", dir)
				cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
				if err := cmd.Start(); err != nil {
					s.notice(noticeWarn, "Restart: "+err.Error())
					return
				}
				s.app.Quit()
			}, s.win)
	})
}
