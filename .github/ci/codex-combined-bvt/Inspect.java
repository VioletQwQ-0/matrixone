import io.mo.util.ScriptParser;
import io.mo.util.ResultParser;
import io.mo.cases.TestScript;
import io.mo.cases.SqlCommand;
import java.util.*;
public class Inspect {
  public static void main(String[] args) {
    TestScript s = new ScriptParser().parseScript(args[0]);
    var p = ResultParser.loadExpectedResultsFromFile(s);
    Map<Integer,Integer> counts = new TreeMap<>();
    Map<Integer,Integer> positions = new TreeMap<>();
    for (SqlCommand c: s.getCommands()) { counts.merge(c.getConn_id(), 1, Integer::sum); positions.put(c.getPosition(),c.getConn_id()); }
    System.out.println("PARSE=" + p.isSuccess() + " error=" + p.getErrorMessage());
    System.out.println("TOTAL=" + s.getTotalCmdCount() + " SESSIONS=" + counts + " POSITIONS=" + positions);
    boolean mapping = Objects.equals(positions.get(26),1) && Objects.equals(positions.get(51),1) && Objects.equals(positions.get(57),3) && Objects.equals(positions.get(90),4);
    if (!p.isSuccess() || s.getTotalCmdCount()!=77 || !counts.toString().equals("{0=59, 1=4, 2=2, 3=3, 4=5, 5=2, 6=2}") || !mapping) System.exit(2);
  }
}
