package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"os"
	"regexp"
	"strconv"
	"strings"

	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/rimage"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/vision/viscapture"

	"github.com/erh/vmodutils"

	"viamchess"
)

func main() {
	err := realMain()
	if err != nil {
		panic(err)
	}
}

func realMain() error {
	ctx := context.Background()
	logger := logging.NewLogger("cli")

	host := flag.String("host", "", "host")
	debug := flag.Bool("debug", false, "")
	cmd := flag.String("cmd", "", "command to execute (move, go, reset, wipe, skill, etc..)")

	from := flag.String("from", "", "")
	to := flag.String("to", "", "")
	n := flag.Int("n", 1, "")

	flag.Parse()

	if *debug {
		logger.SetLevel(logging.DEBUG)
	}

	if *host == "" {
		return fmt.Errorf("need a host")
	}

	if *cmd == "" {
		return fmt.Errorf("need command")
	}

	machine, err := vmodutils.ConnectToHostFromCLIToken(ctx, *host, logger)
	if err != nil {
		return err
	}
	defer machine.Close(ctx)

	deps, err := vmodutils.MachineToDependencies(machine)
	if err != nil {
		return err
	}

	if *cmd == "capture-board" {
		return captureBoard(ctx, deps, logger)
	}

	if *cmd == "piece-finder" {
		pf, err := viamchess.NewPieceFinder(ctx, deps, generic.Named("foo"), &viamchess.PieceFinderConfig{Input: "cam"}, logger)
		if err != nil {
			return err
		}
		all, err := pf.CaptureAllFromCamera(ctx, "cam", viscapture.CaptureOptions{},
			map[string]interface{}{"debug": true, "save": true})
		if err != nil {
			return err
		}
		logger.Infof("Detections    : %d %v", len(all.Detections), all.Detections)
		logger.Infof("Classification: %d %v", len(all.Classifications), all.Classifications)
		logger.Infof("Objects       : %d %v", len(all.Objects), all.Objects)

		if *from != "" {
			for _, o := range all.Objects {
				if strings.HasPrefix(o.Geometry.Label(), *from) {
					logger.Infof("%s : %v", *from, viamchess.GetPickupCenter(o))
					fn := fmt.Sprintf("piece-%s.pcd", *from)
					if f, err := os.Create(fn); err != nil {
						logger.Errorf("failed to create %s: %v", fn, err)
					} else {
						defer f.Close()
						if err = pointcloud.ToPCD(o, f, pointcloud.PCDBinary); err != nil {
							return fmt.Errorf("failed to write %s: %w", fn, err)
						}
						logger.Infof("wrote point cloud to %s", fn)
					}
				}
			}
		}
		return nil
	}

	cfg := viamchess.ChessConfig{
		PieceFinder: "piece-finder",
		Arm:         "arm",
		Gripper:     "gripper",
		PoseStart:   "hack-pose-look-straight-down",
		Camera:      "cam",
		CaptureDir:  "captured-data",
	}
	_, _, err = cfg.Validate("")
	if err != nil {
		return err
	}

	thing, err := viamchess.NewChess(ctx, deps, generic.Named("foo"), &cfg, logger)
	if err != nil {
		return err
	}
	defer thing.Close(ctx)

	switch *cmd {
	case "move":
		res, err := thing.DoCommand(ctx, map[string]interface{}{
			"move": map[string]interface{}{"from": *from, "to": *to, "n": *n},
		})
		if err != nil {
			return err
		}
		logger.Infof("res: %v", res)
		return nil
	case "hover":
		res, err := thing.DoCommand(ctx, map[string]interface{}{
			"hover": *from,
		})
		if err != nil {
			return err
		}
		logger.Infof("res: %v", res)
		return nil

	case "go":
		res, err := thing.DoCommand(ctx, map[string]interface{}{
			"go": *n,
		})
		if err != nil {
			return err
		}
		logger.Infof("res: %v", res)
		return nil
	case "reset":
		res, err := thing.DoCommand(ctx, map[string]interface{}{
			"reset": true,
		})
		if err != nil {
			return err
		}
		logger.Infof("res: %v", res)
		return nil

	default:
		return fmt.Errorf("unknown command [%s]", *cmd)
	}

	return nil
}

// captureBoard fetches a single image from the configured camera, saves it as the
// next available data/boardNN.jpg, runs the board-finder on it, and appends a test
// case to board_finder_test.go using the detected corners. The new test serves both
// as a regression check and as a baseline if the corners later need manual tweaking.
func captureBoard(ctx context.Context, deps resource.Dependencies, logger logging.Logger) error {
	cam, err := camera.FromProvider(deps, "cam")
	if err != nil {
		return fmt.Errorf("get camera: %w", err)
	}

	ni, _, err := cam.Images(ctx, nil, nil)
	if err != nil {
		return fmt.Errorf("camera Images: %w", err)
	}
	if len(ni) == 0 {
		return fmt.Errorf("camera returned no images")
	}
	img, err := ni[0].Image(ctx)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}

	const dataDir = "data"
	const testFile = "board_finder_test.go"
	const defaultTolerance = 3.5

	num, err := nextBoardNumber(dataDir)
	if err != nil {
		return err
	}

	imagePath := fmt.Sprintf("%s/board%d.jpg", dataDir, num)
	if err := rimage.WriteImageToFile(imagePath, img); err != nil {
		return fmt.Errorf("save %s: %w", imagePath, err)
	}
	logger.Infof("saved %s", imagePath)

	corners, err := viamchess.FindBoard(img)
	if err != nil {
		return fmt.Errorf("FindBoard: %w", err)
	}
	if len(corners) != 4 {
		return fmt.Errorf("FindBoard returned %d corners, expected 4", len(corners))
	}
	logger.Infof("corners: TL=%v TR=%v BR=%v BL=%v",
		corners[0], corners[1], corners[2], corners[3])

	if err := appendBoardTestCase(testFile, num, corners, defaultTolerance); err != nil {
		return fmt.Errorf("update %s: %w", testFile, err)
	}
	logger.Infof("appended board%d test case to %s", num, testFile)
	return nil
}

// nextBoardNumber returns one greater than the highest N in dataDir/boardN.jpg.
func nextBoardNumber(dataDir string) (int, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", dataDir, err)
	}
	re := regexp.MustCompile(`^board(\d+)\.jpg$`)
	maxN := 0
	for _, e := range entries {
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > maxN {
			maxN = n
		}
	}
	return maxN + 1, nil
}

// appendBoardTestCase inserts a new {inputFile, expectedCorners, tolerance} entry
// at the end of the testCases slice in board_finder_test.go. It anchors on the
// closing brace of that slice, so it does not depend on any specific neighbor entry.
func appendBoardTestCase(path string, num int, corners []image.Point, tolerance float64) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	const marker = "\n\t}\n\n\tfor _, tc := range testCases"
	idx := strings.Index(string(data), marker)
	if idx < 0 {
		return fmt.Errorf("could not find testCases close marker in %s", path)
	}

	entry := fmt.Sprintf(`		{
			inputFile: "data/board%d.jpg",
			expectedCorners: []image.Point{
				{%d, %d},   // top-left
				{%d, %d},   // top-right
				{%d, %d}, // bottom-right
				{%d, %d}, // bottom-left
			},
			tolerance: %g,
		},
`,
		num,
		corners[0].X, corners[0].Y,
		corners[1].X, corners[1].Y,
		corners[2].X, corners[2].Y,
		corners[3].X, corners[3].Y,
		tolerance,
	)

	out := string(data[:idx+1]) + entry + string(data[idx+1:])
	return os.WriteFile(path, []byte(out), 0o644)
}
