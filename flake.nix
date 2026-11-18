{
  inputs = {
    nixpkgs.url = "https://nixos.org/channels/nixpkgs-unstable/nixexprs.tar.xz";
  };

  outputs =
    { nixpkgs, ... }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAll = f: nixpkgs.lib.genAttrs systems (s: f s nixpkgs.legacyPackages.${s});
    in
    {
      packages = forAll (
        _: pkgs: {
          default = pkgs.buildGoModule {
            pname = "opencc";
            version = "0.1.0";
            src = ./.;
            vendorHash = null; # no Go dependencies
            subPackages = [ "cmd/opencc" ];
            meta.mainProgram = "opencc";
          };
        }
      );

      devShells = forAll (
        system: pkgs: {
          default = pkgs.mkShell {
            packages = [
              pkgs.go
              pkgs.gopls
              pkgs.golangci-lint
              pkgs.just
              pkgs.jq
            ];
          };
        }
      );

      formatter = forAll (_: pkgs: pkgs.nixfmt);
    };
}
