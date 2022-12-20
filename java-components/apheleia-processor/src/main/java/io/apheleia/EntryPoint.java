package io.apheleia;

import io.quarkus.picocli.runtime.annotations.TopCommand;
import picocli.CommandLine;

@TopCommand
@CommandLine.Command(mixinStandardHelpOptions = true, subcommands = {
        DeployCommand.class,
        AnalyserCommand.class,
        DownloadSources.class
})
public class EntryPoint {
}
