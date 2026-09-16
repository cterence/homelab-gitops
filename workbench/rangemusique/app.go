package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/h2non/filetype"
	"github.com/michiwend/gomusicbrainz"
	jellyfin "github.com/sj14/jellyfin-go/api"
	"go.senan.xyz/taglib"
	"golift.io/starr"
	"golift.io/starr/lidarr"
)

type album struct {
	title          string
	artist         string
	tracks         []track
	year           string
	coverImagePath string
}

type track struct {
	title  string
	number string
	path   string
}

const (
	UNKNOWN_ARTIST      = "Unknown Artist"
	UNKNOWN_ALBUM       = "Unknown Album"
	UNKNOWN_TITLE       = "Unknown Title"
	UNKNOWN_TRACKNUMBER = "0"
	UNKNOWN_YEAR        = "0"
)

func Run(ctx context.Context, cfg Config) error {
	locked, err := checkLockFile(cfg.LockFile)
	if err != nil {
		return fmt.Errorf("failed to check lock file: %w", err)
	}

	if locked {
		slog.Info("lock file detected, skipping run", "lockFile", cfg.LockFile)
		return nil
	}

	err = initApp(&cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize app: %w", err)
	}

	trackFilePaths := []string{}
	coverImagePath := []string{}

	err = filepath.WalkDir(cfg.InputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		buf, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", path, err)
		}

		if filetype.IsAudio(buf) {
			trackFilePaths = append(trackFilePaths, path)
		}

		if filetype.IsImage(buf) {
			coverImagePath = append(coverImagePath, path)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to read input directory files: %w", err)
	}

	if len(trackFilePaths) == 0 {
		slog.Info("no music files found in input directory, exiting")
		return nil
	}

	albums, err := buildAlbums(trackFilePaths, coverImagePath)
	if err != nil {
		return fmt.Errorf("failed to build albums: %w", err)
	}

	for _, a := range albums {
		outDirPath := getOutDirPath(cfg.OutputDir, a.artist, a.title, a.year)

		err = os.MkdirAll(outDirPath, os.ModePerm)
		if err != nil {
			return fmt.Errorf("failed to create output directory %s: %w", outDirPath, err)
		}

		for _, t := range a.tracks {
			trackFileName := filepath.Base(t.path)

			if a.year == UNKNOWN_YEAR && a.artist != UNKNOWN_ARTIST && a.title != UNKNOWN_ALBUM {
				releaseYear, err := getReleaseYearFromMusicBrainz(cfg.mbClient, a.artist, a.title)
				if err != nil {
					slog.Warn("failed to get release year from MusicBrainz for file", "error", err)
				}

				a.year = releaseYear

				slog.Debug("track elements", "musicFileName", trackFileName, "trackElements", t)
			}

			outfileName := trackFileName
			// Only set the outfileName if the track elements are not unknown
			if a.artist != UNKNOWN_ARTIST && a.title != UNKNOWN_ALBUM && t.number != UNKNOWN_TRACKNUMBER && t.title != UNKNOWN_TITLE {
				outfileName = getOutFileName(a.artist, a.title, t.number, t.title, filepath.Ext(trackFileName))
			}

			slog.Debug("file name", "musicFileName", trackFileName, "name", outfileName)

			// Copy file to output directory
			outputFile := filepath.Join(outDirPath, outfileName)
			slog.Debug("copying track", "path", outputFile)

			if cfg.Copy {
				err = copyFile(t.path, outputFile)
				if err != nil {
					return fmt.Errorf("failed to move track to output directory: %w", err)
				}
			} else {
				err = moveFile(t.path, outputFile)
				if err != nil {
					return fmt.Errorf("failed to copy track to output directory: %w", err)
				}
			}
		}

		if a.coverImagePath != "" {
			outputFile := filepath.Join(outDirPath, filepath.Base(a.coverImagePath))
			slog.Debug("copying cover image", "path", outputFile)

			if cfg.Copy {
				err = copyFile(a.coverImagePath, outputFile)
				if err != nil {
					return fmt.Errorf("failed to move cover image to output directory: %w", err)
				}
			} else {
				err = moveFile(a.coverImagePath, outputFile)
				if err != nil {
					return fmt.Errorf("failed to copy cover image to output directory: %w", err)
				}
			}
		}
	}

	slog.Info("processed all files", "count", len(trackFilePaths))

	if len(trackFilePaths) > 0 {
		if cfg.LidarrURL != "" && cfg.LidarrAPIKey != "" {
			err = rescanLidarrFolders(cfg.LidarrURL, cfg.LidarrAPIKey)
			if err != nil {
				slog.Warn("failed to refresh Lidarr library", "error", err)
			}
		}

		time.Sleep(5 * time.Second) // Wait for a few seconds before refreshing Jellyfin

		if cfg.JellyfinURL != "" && cfg.JellyfinAPIKey != "" {
			err = refreshJellyfinLibrary(ctx, cfg.JellyfinURL, cfg.JellyfinAPIKey)
			if err != nil {
				slog.Warn("failed to refresh Jellyfin library", "error", err)
			}
		}
	}

	return nil
}

func sanitizeTag(tag string) string {
	tag = strings.TrimSpace(tag)
	tag = strings.ReplaceAll(tag, "/", "_")

	return tag
}

// albumKey uniquely identifies an album: two different artists can release
// identically titled albums, so the title alone is not enough.
func albumKey(artist, title string) string {
	return artist + " - " + title
}

func buildAlbums(trackFilePaths, coverImagePaths []string) (map[string]album, error) {
	albums := map[string]album{}

	for _, p := range trackFilePaths {
		tags, err := taglib.ReadTags(p)
		if err != nil {
			return nil, fmt.Errorf("failed to read tags from file %s: %w", p, err)
		}

		slog.Debug("tags", "trackFilePath", p, "tags", tags)

		albumTitle, albumArtist, albumYear := "", "", ""

		albumsTag, ok := tags["ALBUM"]
		if ok {
			albumTitle = sanitizeTag(albumsTag[0])
		} else {
			albumTitle = UNKNOWN_ALBUM
		}

		artists, ok := tags["ARTIST"]
		if ok {
			albumArtist = sanitizeTag(artists[0])
		} else {
			albumArtist = UNKNOWN_ARTIST
		}

		date, ok := tags["DATE"]
		if ok {
			dateFormat := "2006-01-02"
			if len(date[0]) == 4 {
				dateFormat = "2006"
			}

			formattedDate, err := time.Parse(dateFormat, date[0])
			if err != nil {
				return nil, fmt.Errorf("failed to parse date %s: %w", date[0], err)
			}

			albumYear = strconv.Itoa(formattedDate.Year())
		} else {
			albumYear = UNKNOWN_YEAR
		}

		currentAlbum := album{}
		key := albumKey(albumArtist, albumTitle)

		if existing, ok := albums[key]; ok {
			currentAlbum = existing
		} else {
			currentAlbum.title = albumTitle
			currentAlbum.year = albumYear
			currentAlbum.artist = albumArtist

			coverImageIndex := slices.IndexFunc(coverImagePaths, func(c string) bool {
				return filepath.Dir(c) == filepath.Dir(p)
			})

			if coverImageIndex != -1 {
				currentAlbum.coverImagePath = coverImagePaths[coverImageIndex]
			}
		}

		track, err := getTrackElementsFromTags(tags)
		if err != nil {
			return nil, err
		}

		track.path = p

		currentAlbum.tracks = append(currentAlbum.tracks, track)

		albums[key] = currentAlbum
	}

	return albums, nil
}

func getTrackElementsFromTags(tags map[string][]string) (track, error) {
	var te track

	titles, ok := tags["TITLE"]
	if ok {
		te.title = sanitizeTag(titles[0])
	} else {
		te.title = UNKNOWN_TITLE
	}

	trackNumbers, ok := tags["TRACKNUMBER"]
	if ok {
		trackNumber := sanitizeTag(trackNumbers[0])
		re := regexp.MustCompile(`\d+`)
		trackNumber = re.FindString(trackNumber)
		te.number = trackNumber
	} else {
		te.number = UNKNOWN_TRACKNUMBER
	}

	return te, nil
}

func getOutDirPath(outDir, artist, album, year string) string {
	yearString := ""
	if year != UNKNOWN_YEAR {
		yearString = fmt.Sprintf(" (%s)", year)
	}

	return filepath.Join(outDir, artist, fmt.Sprintf("%s%s", album, yearString))
}

func getOutFileName(artist, albumTitle, trackNumber, title, ext string) string {
	return fmt.Sprintf("%s - %s - %s %s%s", artist, albumTitle, trackNumber, title, ext)
}

func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", src, err)
	}

	err = os.WriteFile(dst, input, 0644)
	if err != nil {
		return fmt.Errorf("failed to write file %s: %w", dst, err)
	}

	return nil
}

func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return moveCrossDevice(src, dst)
		}

		return fmt.Errorf("failed to move file %s to %s: %w", src, dst, err)
	}

	return nil
}

func moveCrossDevice(source, destination string) error {
	src, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", source, err)
	}

	dst, err := os.Create(destination)
	if err != nil {
		err := src.Close()
		if err != nil {
			return fmt.Errorf("failed to close source file %s: %w", source, err)
		}

		return fmt.Errorf("failed to create destination file %s: %w", destination, err)
	}

	_, err = io.Copy(dst, src)
	if err != nil {
		return fmt.Errorf("failed to copy file %s to %s: %w", source, destination, err)
	}

	err = src.Close()
	if err != nil {
		return fmt.Errorf("failed to close source file %s: %w", source, err)
	}

	err = dst.Close()
	if err != nil {
		return fmt.Errorf("failed to close destination file %s: %w", destination, err)
	}

	fi, err := os.Stat(source)
	if err != nil {
		err = os.Remove(destination)
		if err != nil {
			return fmt.Errorf("failed to remove destination file %s: %w", destination, err)
		}

		return fmt.Errorf("failed to stat destination file %s: %w", destination, err)
	}

	err = os.Chmod(destination, fi.Mode())
	if err != nil {
		err = os.Remove(destination)
		if err != nil {
			return fmt.Errorf("failed to remove destination file %s: %w", destination, err)
		}

		return fmt.Errorf("failed to chmod destination file %s: %w", destination, err)
	}

	err = os.Remove(source)
	if err != nil {
		return fmt.Errorf("failed to remove source file %s: %w", source, err)
	}

	return nil
}

func getReleaseYearFromMusicBrainz(mb *gomusicbrainz.WS2Client, artist, album string) (string, error) {
	rgQuery := fmt.Sprintf(`artist:"%s" AND releasegroup:"%s"`, artist, album)

	rgResp, err := mb.SearchReleaseGroup(rgQuery, -1, -1)
	if err != nil {
		return "", fmt.Errorf("failed to search MusicBrainz: %w", err)
	}

	if len(rgResp.ReleaseGroups) == 0 {
		slog.Warn("no release group found", "artist", artist, "album", album)
		return "", nil
	}

	return strconv.Itoa(rgResp.ReleaseGroups[0].FirstReleaseDate.Year()), nil
}

func refreshJellyfinLibrary(ctx context.Context, jellyfinURL, jellyfinAPIKey string) error {
	config := &jellyfin.Configuration{
		Servers:       jellyfin.ServerConfigurations{{URL: jellyfinURL}},
		DefaultHeader: map[string]string{"Authorization": fmt.Sprintf(`MediaBrowser Token="%s"`, jellyfinAPIKey)},
	}
	jc := jellyfin.NewAPIClient(config)

	// Trigger library scan
	_, err := jc.LibraryAPI.RefreshLibrary(ctx).Execute()
	if err != nil {
		return fmt.Errorf("failed to refresh Jellyfin library: %w", err)
	}

	slog.Info("Jellyfin library refresh triggered")

	return nil
}

func rescanLidarrFolders(lidarrURL, lidarrAPIKey string) error {
	c := starr.New(lidarrURL, lidarrAPIKey, 5*time.Second)
	l := lidarr.New(c)

	command := &lidarr.CommandRequest{
		Name: "RescanFolders",
	}

	resp, err := l.SendCommand(command)
	if err != nil {
		return fmt.Errorf("failed to send RescanFolders command to Lidarr: %w", err)
	}

	slog.Info("Lidarr folder rescan triggered", "status", resp.Status)

	return nil
}
