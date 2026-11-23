package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fabiotnt/musafoz/internal/media"
)

// newLicensesCommand prints the notices of third-party components built
// into musafoz. The notices are embedded with the components, so every
// copy of the binary carries them, however it is packaged.
func newLicensesCommand(env Env) *cobra.Command {
	return &cobra.Command{
		Use:   "licenses",
		Short: "Show the licenses of third-party components built into musafoz",
		Long: "Show the licenses of third-party components built into musafoz.\n\n" +
			"The FFmpeg and FFprobe builds that musafoz downloads are not part of it;\n" +
			"their licenses come with their own releases.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := env.Stdout
			fmt.Fprintln(w, "DejaVu Sans Mono 2.37, the default overlay font")
			fmt.Fprintln(w, "Source: https://github.com/dejavu-fonts/dejavu-fonts/releases/tag/version_2_37")
			fmt.Fprintln(w)
			_, err := fmt.Fprint(w, media.EmbeddedFontLicense)
			return err
		},
	}
}
