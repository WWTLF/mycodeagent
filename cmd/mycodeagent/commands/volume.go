package commands

import (
	"fmt"
	"strconv"

	"github.com/WWTLF/mycodeagent/internal/domain/service"
	"github.com/spf13/cobra"
)

func NewVolumeCmd(volumeSvc *service.VolumeService) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage persistent storage volumes",
	}

	cmd.AddCommand(
		newVolumeCreateCmd(volumeSvc),
		newVolumeListCmd(volumeSvc),
		newVolumeDeleteCmd(volumeSvc),
	)

	return cmd
}

func newVolumeCreateCmd(volumeSvc *service.VolumeService) *cobra.Command {
	var sizeGB int

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a persistent volume for model caching",
		RunE: func(cmd *cobra.Command, args []string) error {
			vol, err := volumeSvc.Create(sizeGB)
			if err != nil {
				return err
			}
			fmt.Println()
			fmt.Printf("Volume created successfully:\n")
			fmt.Printf("  ID         : %d\n", vol.ID)
			fmt.Printf("  Name       : %s\n", vol.VolumeName)
			fmt.Printf("  Size       : %d GB\n", vol.SizeGB)
			fmt.Printf("  Mount path : %s\n", vol.MountPath)
			fmt.Printf("  Machine    : %d\n", vol.MachineID)
			fmt.Println()
			fmt.Println("The volume will be auto-attached to new instances.")
			return nil
		},
	}

	cmd.Flags().IntVar(&sizeGB, "size", 50, "Volume size in GB")

	return cmd
}

func newVolumeListCmd(volumeSvc *service.VolumeService) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List persistent volumes",
		RunE: func(cmd *cobra.Command, args []string) error {
			vols, err := volumeSvc.List()
			if err != nil {
				return err
			}
			if len(vols) == 0 {
				fmt.Println("No volumes. Create one with: mycodeagent volume create")
				return nil
			}
			fmt.Printf("%-4s %-10s %-6s %-30s %s\n", "ID", "Name", "Size", "Mount Path", "Machine")
			for _, v := range vols {
				fmt.Printf("%-4d %-10s %-4dGB %-30s %d\n", v.ID, v.VolumeName, v.SizeGB, v.MountPath, v.MachineID)
			}
			return nil
		},
	}
}

func newVolumeDeleteCmd(volumeSvc *service.VolumeService) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a persistent volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid volume ID: %w", err)
			}
			if err := volumeSvc.Delete(id); err != nil {
				return err
			}
			fmt.Printf("Volume %d deleted.\n", id)
			return nil
		},
	}
}
